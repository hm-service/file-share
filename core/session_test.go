package core

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSessionIssueVerifyRoundTrip(t *testing.T) {
	key := []byte("test_signing_key_0123456789")
	signer := NewSessionSigner(key)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	token, expiresAt, err := signer.Issue(now, "alice")
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if want := now.Add(SessionTTL).Unix(); expiresAt != want {
		t.Errorf("expires_at = %d，想要 %d", expiresAt, want)
	}

	// 形状：base64url(exp|nonce|user).base64url(HMAC)，两段都不带填充。
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("凭据应当有两段: %q", token)
	}
	plain, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("载荷不是 base64url: %v", err)
	}
	rawExpires, rest, found := strings.Cut(string(plain), "|")
	if !found {
		t.Fatalf("载荷不是 exp|nonce|user 形状: %q", plain)
	}
	rawNonce, username, found := strings.Cut(rest, "|")
	if !found || username != "alice" {
		t.Fatalf("载荷里的用户名 = %q: %q", username, plain)
	}
	if got, _ := strconv.ParseInt(rawExpires, 10, 64); got != expiresAt {
		t.Errorf("载荷里的过期时间 = %d，想要 %d", got, expiresAt)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(rawNonce)
	if err != nil || len(nonce) != 16 {
		t.Errorf("nonce 应当是 16 字节的 base64url: len=%d err=%v", len(nonce), err)
	}

	gotExpires, gotUser, err := signer.Verify(token, now)
	if err != nil {
		t.Fatalf("Verify() 报错: %v", err)
	}
	if gotExpires != expiresAt || gotUser != "alice" {
		t.Errorf("Verify() = (%d, %q)，想要 (%d, %q)", gotExpires, gotUser, expiresAt, "alice")
	}
	// 临近过期（TTL 最后 1 秒）仍然有效。
	if _, _, err := signer.Verify(token, time.Unix(expiresAt-1, 0)); err != nil {
		t.Errorf("过期前一刻应当有效: %v", err)
	}
}

func TestSessionVerifyRejectsExpired(t *testing.T) {
	signer := NewSessionSigner([]byte("test_signing_key_0123456789"))
	now := time.Now()
	token, expiresAt, err := signer.Issue(now, "alice")
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, _, err := signer.Verify(token, time.Unix(expiresAt, 0)); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("过期一秒应当返回 ErrSessionExpired，得到 %v", err)
	}
	if _, _, err := signer.Verify(token, now.Add(SessionTTL+time.Hour)); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("过期一小时后应当返回 ErrSessionExpired，得到 %v", err)
	}
}

// 验签必须发生在看过期之前：被篡改的过期凭据是「格式无效」，不是「过期」。
func TestSessionVerifiesSignatureBeforeExpiry(t *testing.T) {
	signer := NewSessionSigner([]byte("test_signing_key_0123456789"))
	token, _, err := signer.Issue(time.Now().Add(-SessionTTL-time.Hour), "alice")
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, _, err := signer.Verify(tamperPayload(token), time.Now()); !errors.Is(err, ErrSessionMalformed) {
		t.Errorf("被篡改的凭据应当返回 ErrSessionMalformed，得到 %v", err)
	}
}

func TestSessionVerifyRejectsMalformed(t *testing.T) {
	key := []byte("test_signing_key_0123456789")
	signer := NewSessionSigner(key)
	valid, _, err := signer.Issue(time.Now(), "alice")
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	// 自己用同一把钥匙签几个字段不合约定的载荷：签名是对的，但这些凭据必须被拒。
	sign := func(plain string) string {
		payload := base64.RawURLEncoding.EncodeToString([]byte(plain))
		return payload + "." + base64.RawURLEncoding.EncodeToString(signer.sign(payload))
	}
	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	nonce := base64.RawURLEncoding.EncodeToString([]byte("sixteen-bytes-xx"))

	cases := []struct {
		name  string
		token string
	}{
		{name: "空串", token: ""},
		{name: "只有点", token: "."},
		{name: "没有签名", token: valid[:strings.Index(valid, ".")]},
		{name: "没有载荷", token: valid[strings.Index(valid, "."):]},
		{name: "多一段", token: valid + "." + valid},
		{name: "载荷不是 base64", token: "@@@." + valid[strings.Index(valid, ".")+1:]},
		{name: "签名不是 base64", token: valid[:strings.Index(valid, ".")] + ".@@@"},
		{name: "签名少一个字符", token: valid[:len(valid)-1]},
		{name: "载荷被改", token: "x" + valid[1:]},
		{name: "只有 exp 没有 nonce", token: sign(future)},
		{name: "只有 exp 与 nonce", token: sign(future + "|" + nonce)},
		{name: "用户名超长", token: sign(future + "|" + nonce + "|" + strings.Repeat("a", 65))},
		{name: "用户名带分隔符", token: sign(future + "|" + nonce + "|ali|ce")},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, _, err := signer.Verify(item.token, time.Now()); !errors.Is(err, ErrSessionMalformed) {
				t.Errorf("Verify(%q) = %v，想要 ErrSessionMalformed", item.token, err)
			}
		})
	}
}

func TestSessionDifferentKeyRejected(t *testing.T) {
	issuer := NewSessionSigner([]byte("key_one_0123456789"))
	verifier := NewSessionSigner([]byte("key_two_0123456789"))
	token, _, err := issuer.Issue(time.Now(), "alice")
	if err != nil {
		t.Fatalf("Issue() 报错: %v", err)
	}
	if _, _, err := verifier.Verify(token, time.Now()); !errors.Is(err, ErrSessionMalformed) {
		t.Errorf("换一把钥匙应当校验失败，得到 %v", err)
	}
}

// tamperPayload 改掉载荷的第一个字符：签名必然对不上，用来验证「先验签」。
func tamperPayload(token string) string {
	if token[0] == 'A' {
		return "B" + token[1:]
	}
	return "A" + token[1:]
}
