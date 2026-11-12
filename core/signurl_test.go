package core

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignedURLRoundTrip(t *testing.T) {
	key := []byte("url_signature_key_0123456789abc")
	signer := NewURLSigner(key)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	exp := now.Add(10 * time.Minute).Unix()

	url := signer.SignedURL("/s/013579bd", exp)
	prefix, query, found := strings.Cut(url, "?")
	if !found || prefix != "/s/013579bd" {
		t.Fatalf("签名 URL 形状不对: %q", url)
	}
	// 参数固定 sig 在前、exp 在后，两者都不带填充。
	values := strings.Split(query, "&")
	if len(values) != 2 || !strings.HasPrefix(values[0], "sig=") || values[1] != "exp="+strconv.FormatInt(exp, 10) {
		t.Fatalf("查询参数形状不对: %q", query)
	}
	sig := strings.TrimPrefix(values[0], "sig=")
	if len(sig) != 43 || strings.ContainsAny(sig, "=+/") {
		t.Errorf("sig 应当是 43 字符的 base64url（32 字节、无填充）: %q", sig)
	}

	if err := signer.Verify("GET", "/s/013579bd", sig, exp, now); err != nil {
		t.Fatalf("Verify() 报错: %v", err)
	}
	// 下载管理器先发 HEAD：与 GET 共用同一份签名。
	if err := signer.Verify("HEAD", "/s/013579bd", sig, exp, now); err != nil {
		t.Errorf("HEAD 应当与 GET 共用签名: %v", err)
	}
	// 过期前一刻仍然有效，到点即失效。
	if err := signer.Verify("GET", "/s/013579bd", sig, exp, time.Unix(exp-1, 0)); err != nil {
		t.Errorf("过期前一刻应当有效: %v", err)
	}
	if err := signer.Verify("GET", "/s/013579bd", sig, exp, time.Unix(exp, 0)); !errors.Is(err, ErrSignedURLExpired) {
		t.Errorf("到点应当过期，err = %v", err)
	}
}

func TestSignedURLRejectsTampering(t *testing.T) {
	key := []byte("url_signature_key_0123456789abc")
	signer := NewURLSigner(key)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	exp := now.Add(10 * time.Minute).Unix()
	sig := signer.Sign("/s/013579bd", exp)

	cases := []struct {
		name   string
		method string
		path   string
		sig    string
		exp    int64
	}{
		{name: "换文件", method: "GET", path: "/s/013579be", sig: sig, exp: exp},
		{name: "换路径前缀", method: "GET", path: "/x/013579bd", sig: sig, exp: exp},
		{name: "改过期时间", method: "GET", path: "/s/013579bd", sig: sig, exp: exp + 60},
		{name: "换方法", method: "POST", path: "/s/013579bd", sig: sig, exp: exp},
		{name: "空签名", method: "GET", path: "/s/013579bd", sig: "", exp: exp},
		{name: "签名不是 base64url", method: "GET", path: "/s/013579bd", sig: "!!!", exp: exp},
		{name: "签名长度不对", method: "GET", path: "/s/013579bd", sig: "c2hvcnQ", exp: exp},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := signer.Verify(item.method, item.path, item.sig, item.exp, now)
			if !errors.Is(err, ErrSignedURLMalformed) {
				t.Fatalf("应当按伪造拒绝，err = %v", err)
			}
		})
	}

	// 换密钥签出的 sig 也验不过。
	other := NewURLSigner([]byte("another_url_signature_key_0123456789"))
	if err := signer.Verify("GET", "/s/013579bd", other.Sign("/s/013579bd", exp), exp, now); err == nil {
		t.Fatal("别的密钥签出的签名不该通过")
	}
}

func TestDownloadURLExpiryRoundsUpToNextUTCHour(t *testing.T) {
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), want: time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)},
		{now: time.Date(2026, 11, 12, 12, 0, 1, 0, time.UTC), want: time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)},
		{now: time.Date(2026, 11, 12, 12, 59, 59, 0, time.UTC), want: time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)},
		{now: time.Date(2026, 11, 12, 23, 30, 0, 0, time.UTC), want: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)},
	}
	for _, item := range cases {
		if got := DownloadURLExpiry(item.now); got != item.want.Unix() {
			t.Errorf("DownloadURLExpiry(%s) = %d，想要 %d", item.now, got, item.want.Unix())
		}
	}
	// 本地时区不影响取整结果：结果始终是 UTC 整点。
	local := time.Date(2026, 11, 12, 20, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if got := DownloadURLExpiry(local); got != time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("本地时间取整结果 = %d，想要 UTC 13:00", got)
	}
}

func TestShareURLExpiryUsesTTL(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 34, 56, 0, time.UTC)
	if got, want := ShareURLExpiry(now), now.Add(ShareLinkTTL).Unix(); got != want {
		t.Errorf("ShareURLExpiry() = %d，想要 %d", got, want)
	}
	if ShareLinkTTL != 10*time.Minute {
		t.Errorf("分享链接有效期 = %s，约定是 10 分钟", ShareLinkTTL)
	}
}
