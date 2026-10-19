package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// 会话是无状态的：服务端不存会话表，凭据自带过期时间，用 SESSION_SIGNING_KEY 签名。
// 代价是签出去的凭据撤不回来 —— 只能轮换密钥把所有人下线。
const (
	SessionTTL      = 30 * 24 * time.Hour
	sessionNonceLen = 16
	sessionVersion  = "v1"
)

// 凭据要原样塞进 Authorization 头，所以载荷与签名都用不带填充的 base64url。
var sessionBase64 = base64.RawURLEncoding

var (
	ErrSessionMalformed = errors.New("malformed session credential")
	ErrSessionExpired   = errors.New("expired session credential")
)

// SessionSigner 签发与校验会话凭据：
//
//	payload = base64url("<exp>|<nonce>")，nonce 是 16 字节随机数的 base64url
//	sig     = base64url(HMAC-SHA256(key, "v1|" + payload))
//	token   = payload + "." + sig
type SessionSigner struct {
	key []byte
}

func NewSessionSigner(key []byte) *SessionSigner { return &SessionSigner{key: key} }

// Issue 签一张凭据，返回凭据与它的过期时间（unix 秒）。
func (s *SessionSigner) Issue(now time.Time) (token string, expiresAt int64, err error) {
	nonce, err := RandomBytes(sessionNonceLen)
	if err != nil {
		return "", 0, err
	}
	expiresAt = now.Add(SessionTTL).Unix()
	plain := strconv.FormatInt(expiresAt, 10) + "|" + sessionBase64.EncodeToString(nonce)
	payload := sessionBase64.EncodeToString([]byte(plain))
	return payload + "." + sessionBase64.EncodeToString(s.sign(payload)), expiresAt, nil
}

// Verify 校验凭据并返回过期时间。先验签、再看字段与过期：签名不对的凭据连字段都不值得解析。
func (s *SessionSigner) Verify(token string, now time.Time) (int64, error) {
	payload, signature, found := strings.Cut(token, ".")
	if !found || payload == "" || signature == "" || strings.Contains(signature, ".") {
		return 0, ErrSessionMalformed
	}
	got, err := sessionBase64.DecodeString(signature)
	if err != nil || subtle.ConstantTimeCompare(got, s.sign(payload)) != 1 {
		return 0, ErrSessionMalformed
	}
	raw, err := sessionBase64.DecodeString(payload)
	if err != nil {
		return 0, ErrSessionMalformed
	}
	rawExpires, rawNonce, found := strings.Cut(string(raw), "|")
	if !found {
		return 0, ErrSessionMalformed
	}
	expiresAt, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil {
		return 0, ErrSessionMalformed
	}
	nonce, err := sessionBase64.DecodeString(rawNonce)
	if err != nil || len(nonce) != sessionNonceLen {
		return 0, ErrSessionMalformed
	}
	if now.Unix() >= expiresAt {
		return 0, ErrSessionExpired
	}
	return expiresAt, nil
}

// sign 签的是 token 里那段原文：这样不存在「解析再拼回去」的歧义。
func (s *SessionSigner) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(sessionVersion + "|" + payload))
	return mac.Sum(nil)
}
