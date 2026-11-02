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
//	payload = base64url("<exp>|<file_id>")
//	sig     = base64url(HMAC-SHA256(key, "d1|" + payload))
//	ticket  = payload + "." + sig
//
// 与会话凭据共用密钥，但签名前缀不同，两者不能互相冒用。
const DownloadTicketTTL = 60 * time.Second

const downloadTicketVersion = "d1"

var (
	ErrTicketMalformed = errors.New("malformed download ticket")
	ErrTicketExpired   = errors.New("expired download ticket")
)

// TicketSigner 签发与校验短期下载票据。
type TicketSigner struct {
	key []byte
}

func NewTicketSigner(key []byte) *TicketSigner { return &TicketSigner{key: key} }

// Issue 给一条文件记录签票据。fileID 为空说明调用方有 bug，当场失败，
// 不要把错误拖到用户点下载的时候。
func (s *TicketSigner) Issue(fileID string, now time.Time) (string, error) {
	if fileID == "" {
		return "", errors.New("empty download ticket file id")
	}
	expiresAt := now.Add(DownloadTicketTTL).Unix()
	plain := strconv.FormatInt(expiresAt, 10) + "|" + fileID
	payload := sessionBase64.EncodeToString([]byte(plain))
	return payload + "." + sessionBase64.EncodeToString(s.sign(payload)), nil
}

// Verify 校验票据并返回文件 id：先验签、再解析、最后看过期 —— 签名不对的票据不值得解析。
func (s *TicketSigner) Verify(token string, now time.Time) (string, error) {
	payload, signature, found := strings.Cut(token, ".")
	if !found || payload == "" || signature == "" || strings.Contains(signature, ".") {
		return "", ErrTicketMalformed
	}
	got, err := sessionBase64.DecodeString(signature)
	if err != nil || subtle.ConstantTimeCompare(got, s.sign(payload)) != 1 {
		return "", ErrTicketMalformed
	}
	raw, err := sessionBase64.DecodeString(payload)
	if err != nil {
		return "", ErrTicketMalformed
	}
	rawExpires, fileID, found := strings.Cut(string(raw), "|")
	if !found || fileID == "" || strings.Contains(fileID, "|") {
		return "", ErrTicketMalformed
	}
	expiresAt, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil {
		return "", ErrTicketMalformed
	}
	if now.Unix() >= expiresAt {
		return "", ErrTicketExpired
	}
	return fileID, nil
}

func (s *TicketSigner) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(downloadTicketVersion + "|" + payload))
	return mac.Sum(nil)
}
