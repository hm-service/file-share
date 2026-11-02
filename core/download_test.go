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

	ticket, err := signer.Issue(TicketFile, "file-id-1", now)
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	got, err := signer.Verify(ticket, now)
	if err != nil {
		t.Fatalf("Verify() 报错: %v", err)
	}
	if got.Kind != TicketFile || got.Value != "file-id-1" {
		t.Errorf("票据内容 = %+v，想要 file/file-id-1", got)
	}
	if want := now.Add(DownloadTicketTTL).Unix(); got.ExpiresAt != want {
		t.Errorf("expires_at = %d，想要 %d", got.ExpiresAt, want)
	}
	// 临近过期仍然有效，过期那一刻起失效。
	if _, err := signer.Verify(ticket, time.Unix(got.ExpiresAt-1, 0)); err != nil {
		t.Errorf("过期前一刻应当有效: %v", err)
	}
	if _, err := signer.Verify(ticket, time.Unix(got.ExpiresAt, 0)); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("过期那一刻应当返回 ErrTicketExpired，得到 %v", err)
	}
}

// 分组名允许含 "|"，票据要原样带回，不能被截断。
func TestTicketGroupNameWithSeparator(t *testing.T) {
	signer := NewTicketSigner([]byte("test_signing_key_0123456789"))
	name := "2026|归档|照片"
	ticket, err := signer.Issue(TicketGroup, name, time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	got, err := signer.Verify(ticket, time.Now())
	if err != nil {
		t.Fatalf("Verify() 报错: %v", err)
	}
	if got.Kind != TicketGroup || got.Value != name {
		t.Errorf("票据内容 = %+v，想要 group/%q", got, name)
	}
}

func TestTicketIssueRejectsBadInput(t *testing.T) {
	signer := NewTicketSigner([]byte("test_signing_key_0123456789"))
	if _, err := signer.Issue("other", "x", time.Now()); err == nil {
		t.Error("未知 kind 应当报错")
	}
	if _, err := signer.Issue(TicketFile, "", time.Now()); err == nil {
		t.Error("空 value 应当报错")
	}
}

func TestTicketVerifyRejectsMalformed(t *testing.T) {
	key := []byte("test_signing_key_0123456789")
	signer := NewTicketSigner(key)
	valid, err := signer.Issue(TicketFile, "abc", time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	// 用同一把密钥签一个字段不合约定的载荷：签名正确，但 kind 不认识。
	badKindPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "|other|abc"))
	badKind := badKindPayload + "." + base64.RawURLEncoding.EncodeToString(signer.sign(badKindPayload))
	// 少一段的载荷。
	shortPayload := base64.RawURLEncoding.EncodeToString([]byte("12345|file"))
	short := shortPayload + "." + base64.RawURLEncoding.EncodeToString(signer.sign(shortPayload))

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
		{name: "未知 kind", ticket: badKind},
		{name: "字段不足", ticket: short},
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
	ticket, err := issuer.Issue(TicketFile, "abc", time.Now())
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
	ticket, err := ticketSigner.Issue(TicketFile, "abc", time.Now())
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, err := sessionSigner.Verify(ticket, time.Now()); !errors.Is(err, ErrSessionMalformed) {
		t.Errorf("下载票据不应当通过会话校验，得到 %v", err)
	}
}
