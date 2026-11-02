package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"
	"time"
)

// 下载票据：浏览器直接导航下载才带得动 「Content-Disposition + Range + 原生进度」，
// 但导航带不了 Authorization 头。所以登录后先用 API 换一张短期票据，再让浏览器打开
// 票据 URL —— 凭据不进入 URL，会话 token 也不会出现在地址栏 / 代理日志里。
//
//	payload = base64url("<exp>|<kind>|<value>")   kind = file | group
//	sig     = base64url(HMAC-SHA256(key, "d1|" + payload))
//	ticket  = payload + "." + sig
//
// 与会话凭据共用密钥，但签名前缀不同，两者不能互相冒用。
const DownloadTicketTTL = 60 * time.Second

const downloadTicketVersion = "d1"

// 票据的两种用途：单个文件、整个分组打包。
const (
	TicketFile  = "file"
	TicketGroup = "group"
)

var (
	ErrTicketMalformed = errors.New("malformed download ticket")
	ErrTicketExpired   = errors.New("expired download ticket")
)

// DownloadTicket 是验签后的票据内容。
type DownloadTicket struct {
	Kind      string
	Value     string
	ExpiresAt int64
}

// TicketSigner 签发与校验短期下载票据。
type TicketSigner struct {
	key []byte
}

func NewTicketSigner(key []byte) *TicketSigner { return &TicketSigner{key: key} }

// Issue 签一张票据。kind / value 不接受空值：签出去再校验失败只会把错误拖到下载时。
func (s *TicketSigner) Issue(kind, value string, now time.Time) (string, error) {
	if kind != TicketFile && kind != TicketGroup {
		return "", errors.New("unknown download ticket kind")
	}
	if value == "" {
		return "", errors.New("empty download ticket value")
	}
	expiresAt := now.Add(DownloadTicketTTL).Unix()
	plain := strconv.FormatInt(expiresAt, 10) + "|" + kind + "|" + value
	payload := sessionBase64.EncodeToString([]byte(plain))
	return payload + "." + sessionBase64.EncodeToString(s.sign(payload)), nil
}

// Verify 校验票据并返回内容：先验签、再解析、最后看过期 —— 签名不对的票据不值得解析。
func (s *TicketSigner) Verify(token string, now time.Time) (DownloadTicket, error) {
	payload, signature, found := strings.Cut(token, ".")
	if !found || payload == "" || signature == "" || strings.Contains(signature, ".") {
		return DownloadTicket{}, ErrTicketMalformed
	}
	got, err := sessionBase64.DecodeString(signature)
	if err != nil || subtle.ConstantTimeCompare(got, s.sign(payload)) != 1 {
		return DownloadTicket{}, ErrTicketMalformed
	}
	raw, err := sessionBase64.DecodeString(payload)
	if err != nil {
		return DownloadTicket{}, ErrTicketMalformed
	}
	// value 允许包含 "|"（分组名不限制这个字符），所以只切成三段。
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return DownloadTicket{}, ErrTicketMalformed
	}
	expiresAt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return DownloadTicket{}, ErrTicketMalformed
	}
	if parts[1] != TicketFile && parts[1] != TicketGroup {
		return DownloadTicket{}, ErrTicketMalformed
	}
	if parts[2] == "" {
		return DownloadTicket{}, ErrTicketMalformed
	}
	if now.Unix() >= expiresAt {
		return DownloadTicket{}, ErrTicketExpired
	}
	return DownloadTicket{Kind: parts[1], Value: parts[2], ExpiresAt: expiresAt}, nil
}

func (s *TicketSigner) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(downloadTicketVersion + "|" + payload))
	return mac.Sum(nil)
}
