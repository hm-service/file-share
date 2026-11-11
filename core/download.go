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

const (
	// downloadTicketVersion 是 file 表票据的签名前缀；dav 票据用另一个前缀，
	// 两种票据不能互相冒用。
	downloadTicketVersion    = "d1"
	davDownloadTicketVersion = "d2"
)

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
	return s.issue(downloadTicketVersion, fileID, now)
}

// IssueDav 给一个 WebDAV 路径签票据：浏览器拿它直接导航下载，不需要带 Basic 凭据。
// 路径里可能出现的 "." 与 "|" 都在 base64url 载荷内部，不会破坏票据结构。
func (s *TicketSigner) IssueDav(path string, now time.Time) (string, error) {
	if path == "" {
		return "", errors.New("empty dav download path")
	}
	return s.issue(davDownloadTicketVersion, path, now)
}

func (s *TicketSigner) issue(version, value string, now time.Time) (string, error) {
	expiresAt := now.Add(DownloadTicketTTL).Unix()
	plain := strconv.FormatInt(expiresAt, 10) + "|" + value
	payload := sessionBase64.EncodeToString([]byte(plain))
	return payload + "." + sessionBase64.EncodeToString(s.sign(version, payload)), nil
}

// Verify 校验票据并返回文件 id。
func (s *TicketSigner) Verify(token string, now time.Time) (string, error) {
	raw, err := s.verify(downloadTicketVersion, token, now)
	if err != nil {
		return "", err
	}
	_, fileID, found := strings.Cut(string(raw), "|")
	if !found || fileID == "" || strings.Contains(fileID, "|") {
		return "", ErrTicketMalformed
	}
	return fileID, nil
}

// VerifyDav 校验票据并返回 WebDAV 内部路径。
func (s *TicketSigner) VerifyDav(token string, now time.Time) (string, error) {
	raw, err := s.verify(davDownloadTicketVersion, token, now)
	if err != nil {
		return "", err
	}
	_, path, found := strings.Cut(string(raw), "|")
	if !found || path == "" {
		return "", ErrTicketMalformed
	}
	return path, nil
}

// verify 先验签、再解析、最后看过期 —— 签名不对的票据不值得解析。
func (s *TicketSigner) verify(version, token string, now time.Time) ([]byte, error) {
	payload, signature, found := strings.Cut(token, ".")
	if !found || payload == "" || signature == "" || strings.Contains(signature, ".") {
		return nil, ErrTicketMalformed
	}
	got, err := sessionBase64.DecodeString(signature)
	if err != nil || subtle.ConstantTimeCompare(got, s.sign(version, payload)) != 1 {
		return nil, ErrTicketMalformed
	}
	raw, err := sessionBase64.DecodeString(payload)
	if err != nil {
		return nil, ErrTicketMalformed
	}
	rawExpires, _, found := strings.Cut(string(raw), "|")
	if !found {
		return nil, ErrTicketMalformed
	}
	expiresAt, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil {
		return nil, ErrTicketMalformed
	}
	if now.Unix() >= expiresAt {
		return nil, ErrTicketExpired
	}
	return raw, nil
}

func (s *TicketSigner) sign(version, payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(version + "|" + payload))
	return mac.Sum(nil)
}
