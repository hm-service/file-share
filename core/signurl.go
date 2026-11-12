package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"time"
)

// 签名 URL 是匿名下载的唯一凭据：签名把「方法、路径、过期时间」绑在一起，
// URL 本身就是能力，不需要 Authorization。
//
//	body = "v1|<method>|<escapedPath>|<exp>"
//	sig  = base64url(HMAC-SHA256(URL_SIGNATURE_KEY, body))
//	url  = <escapedPath>?sig=<sig>&exp=<exp>
//
// 签名覆盖 method 与 path：换文件、换方法都验不过；exp 在查询参数里，先验签再判过期。
const (
	// ShareLinkTTL 是分享链接的有效期：够对方打开，又不会让链接长期有效。
	ShareLinkTTL = 10 * time.Minute

	signedURLVersion = "v1"
)

var (
	ErrSignedURLMalformed = errors.New("malformed signed url")
	ErrSignedURLExpired   = errors.New("expired signed url")
)

// URLSigner 签发与校验签名 URL。密钥只用于签名 URL，与会话密钥分开：
// 轮换 URL_SIGNATURE_KEY 只让已签出的链接失效，不会把所有人踢下线。
type URLSigner struct {
	key []byte
}

func NewURLSigner(key []byte) *URLSigner { return &URLSigner{key: key} }

// Sign 给一次 GET 请求签 sig。escapedPath 必须与请求的 EscapedPath 逐字节一致。
func (s *URLSigner) Sign(escapedPath string, exp int64) string {
	return base64.RawURLEncoding.EncodeToString(s.sign("GET", escapedPath, exp))
}

// SignedURL 拼出带签名与过期时间的地址：参数固定为 sig 在前、exp 在后，URL 无需认证。
func (s *URLSigner) SignedURL(escapedPath string, exp int64) string {
	return escapedPath + "?sig=" + s.Sign(escapedPath, exp) + "&exp=" + strconv.FormatInt(exp, 10)
}

// Verify 校验签名 URL：先验签、再判过期。HEAD 与 GET 共用同一份签名 ——
// 下载管理器会先发 HEAD 探长度，没必要为它单独签一次。
func (s *URLSigner) Verify(method, escapedPath, sig string, exp int64, now time.Time) error {
	if sig == "" {
		return ErrSignedURLMalformed
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return ErrSignedURLMalformed
	}
	if method == "HEAD" {
		method = "GET"
	}
	if method != "GET" {
		return ErrSignedURLMalformed
	}
	if subtle.ConstantTimeCompare(got, s.sign(method, escapedPath, exp)) != 1 {
		return ErrSignedURLMalformed
	}
	if now.Unix() >= exp {
		return ErrSignedURLExpired
	}
	return nil
}

func (s *URLSigner) sign(method, escapedPath string, exp int64) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(signedURLVersion + "|" + method + "|" + escapedPath + "|" + strconv.FormatInt(exp, 10)))
	return mac.Sum(nil)
}

// DownloadURLExpiry 是浏览器下载地址的过期时间：取整到下一个 UTC 小时。
// 同一小时内重复点同一个文件签出的地址完全一样，浏览器缓存与下载管理器的重试
// 不会因为重新签名而失效。
func DownloadURLExpiry(now time.Time) int64 {
	return now.UTC().Truncate(time.Hour).Add(time.Hour).Unix()
}

// ShareURLExpiry 是分享链接的过期时间：签发后 ShareLinkTTL 内有效。
func ShareURLExpiry(now time.Time) int64 {
	return now.Add(ShareLinkTTL).Unix()
}
