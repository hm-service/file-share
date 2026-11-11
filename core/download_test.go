package core

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTicketIssueVerifyRoundTrip(t *testing.T) {
	signer := NewTicketSigner([]byte("test_signing_key_0123456789"))
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	ticket, err := signer.Issue("file-id-1", now)
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	fileID, err := signer.Verify(ticket, now)
	if err != nil {
		t.Fatalf("Verify() 报错: %v", err)
	}
	if fileID != "file-id-1" {
		t.Errorf("文件 id = %q，想要 file-id-1", fileID)
	}
	// 临近过期仍然有效，过期那一刻起失效。
	expiresAt := now.Add(DownloadTicketTTL).Unix()
	if _, err := signer.Verify(ticket, time.Unix(expiresAt-1, 0)); err != nil {
		t.Errorf("过期前一刻应当有效: %v", err)
	}
	if _, err := signer.Verify(ticket, time.Unix(expiresAt, 0)); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("过期那一刻应当返回 ErrTicketExpired，得到 %v", err)
	}
}

func TestTicketIssueRejectsEmptyFileID(t *testing.T) {
	signer := NewTicketSigner([]byte("test_signing_key_0123456789"))
	if _, err := signer.Issue("", time.Now()); err == nil {
		t.Error("空 file id 应当报错")
	}
}

func TestTicketVerifyRejectsMalformed(t *testing.T) {
	key := []byte("test_signing_key_0123456789")
	signer := NewTicketSigner(key)
	valid, err := signer.Issue("abc", time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	// 用同一把密钥签一个字段不合约定的载荷：签名正确，但缺 file id。
	missingIDPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "|"))
	missingID := missingIDPayload + "." + base64.RawURLEncoding.EncodeToString(signer.sign(downloadTicketVersion, missingIDPayload))
	// file id 里混进分隔符：解析会歧义，按格式无效处理。
	separatorPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "|abc|def"))
	separator := separatorPayload + "." + base64.RawURLEncoding.EncodeToString(signer.sign(downloadTicketVersion, separatorPayload))

	cases := []struct {
		name   string
		ticket string
	}{
		{name: "空串", ticket: ""},
		{name: "只有点", ticket: "."},
		{name: "没有签名", ticket: valid[:strings.Index(valid, ".")]},
		{name: "没有载荷", ticket: valid[strings.Index(valid, "."):]},
		{name: "多一段", ticket: valid + "." + valid},
		{name: "载荷不是 base64", ticket: "@@@." + valid[strings.Index(valid, ".")+1:]},
		{name: "签名不是 base64", ticket: valid[:strings.Index(valid, ".")] + ".@@@"},
		{name: "签名少一个字符", ticket: valid[:len(valid)-1]},
		{name: "载荷被改", ticket: tamperPayload(valid)},
		{name: "缺 file id", ticket: missingID},
		{name: "file id 含分隔符", ticket: separator},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, err := signer.Verify(item.ticket, time.Now()); !errors.Is(err, ErrTicketMalformed) {
				t.Errorf("Verify(%q) = %v，想要 ErrTicketMalformed", item.ticket, err)
			}
		})
	}
}

func TestTicketDifferentKeyRejected(t *testing.T) {
	issuer := NewTicketSigner([]byte("key_one_0123456789"))
	verifier := NewTicketSigner([]byte("key_two_0123456789"))
	ticket, err := issuer.Issue("abc", time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, err := verifier.Verify(ticket, time.Now()); !errors.Is(err, ErrTicketMalformed) {
		t.Errorf("换一把钥匙应当校验失败，得到 %v", err)
	}
}

// 会话凭据与下载票据共用密钥，但签名前缀不同：互相不能顶替。
func TestTicketAndSessionDoNotInterchange(t *testing.T) {
	key := []byte("test_signing_key_0123456789")
	sessionSigner := NewSessionSigner(key)
	ticketSigner := NewTicketSigner(key)

	session, _, err := sessionSigner.Issue(time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, err := ticketSigner.Verify(session, time.Now()); !errors.Is(err, ErrTicketMalformed) {
		t.Errorf("会话凭据不应当通过票据校验，得到 %v", err)
	}
	ticket, err := ticketSigner.Issue("abc", time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, err := sessionSigner.Verify(ticket, time.Now()); !errors.Is(err, ErrSessionMalformed) {
		t.Errorf("下载票据不应当通过会话校验，得到 %v", err)
	}
}

func TestDavTicketIssueVerifyRoundTrip(t *testing.T) {
	signer := NewTicketSigner([]byte("test_signing_key_0123456789"))
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	for _, path := range []string{"a.txt", "目录/文件 name.txt", "a|b.c d", "x/y/z"} {
		ticket, err := signer.IssueDav(path, now)
		if err != nil {
			t.Fatalf("IssueDav(%q) 报错: %v", path, err)
		}
		got, err := signer.VerifyDav(ticket, now)
		if err != nil {
			t.Fatalf("VerifyDav(%q) 报错: %v", path, err)
		}
		if got != path {
			t.Errorf("路径 = %q，想要 %q", got, path)
		}
	}

	ticket, err := signer.IssueDav("a.txt", now)
	if err != nil {
		t.Fatalf("IssueDav() 报错: %v", err)
	}
	if _, err := signer.Verify(ticket, now); !errors.Is(err, ErrTicketMalformed) {
		t.Errorf("dav 票据不应当通过 file 票据校验，得到 %v", err)
	}
	fileTicket, err := signer.Issue("abc", now)
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, err := signer.VerifyDav(fileTicket, now); !errors.Is(err, ErrTicketMalformed) {
		t.Errorf("file 票据不应当通过 dav 票据校验，得到 %v", err)
	}
	if _, err := signer.VerifyDav(ticket, now.Add(DownloadTicketTTL)); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("过期那一刻应当返回 ErrTicketExpired，得到 %v", err)
	}
	if _, err := signer.IssueDav("", now); err == nil {
		t.Error("空路径应当报错")
	}
}
