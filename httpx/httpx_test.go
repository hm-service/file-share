package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	Write(recorder, http.StatusOK, map[string]any{"ok": true})

	if recorder.Code != http.StatusOK {
		t.Errorf("状态码 = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != ContentType {
		t.Errorf("Content-Type = %q，想要 %q", got, ContentType)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload["ok"] != true {
		t.Errorf("响应体 = %s", recorder.Body.String())
	}
}

func TestFail(t *testing.T) {
	recorder := httptest.NewRecorder()
	Fail(recorder, http.StatusNotFound, "文件不存在")

	if recorder.Code != http.StatusNotFound {
		t.Errorf("状态码 = %d", recorder.Code)
	}
	var payload struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应体不是 JSON: %v", err)
	}
	if payload.Detail != "文件不存在" {
		t.Errorf("detail = %q", payload.Detail)
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}
}

func TestUnauthorized(t *testing.T) {
	recorder := httptest.NewRecorder()
	Unauthorized(recorder, "需要登录")

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("状态码 = %d", recorder.Code)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q", got)
	}
}

func TestStatusRecorder(t *testing.T) {
	recorder := httptest.NewRecorder()
	rec := NewStatusRecorder(recorder)
	if rec.Status() != http.StatusOK {
		t.Errorf("没写响应时状态 = %d，想要 200", rec.Status())
	}
	rec.WriteHeader(http.StatusTeapot)
	rec.Write([]byte("x"))
	if rec.Status() != http.StatusTeapot {
		t.Errorf("状态 = %d，想要 418", rec.Status())
	}
	if rec.Unwrap() != recorder {
		t.Error("Unwrap() 应当返回原始 ResponseWriter")
	}
	if recorder.Code != http.StatusTeapot || recorder.Body.String() != "x" {
		t.Errorf("底层记录器 = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestNewProxies(t *testing.T) {
	t.Run("空串用默认值", func(t *testing.T) {
		proxies, err := NewProxies("")
		if err != nil {
			t.Fatalf("NewProxies() 报错: %v", err)
		}
		if proxies.ClientIP(request("127.0.0.1:1234", "10.0.0.1", "")) != "10.0.0.1" {
			t.Error("默认配置应当信任本机反代头")
		}
	})
	t.Run("IP 与 CIDR 混用", func(t *testing.T) {
		proxies, err := NewProxies("10.0.0.0/8, 192.168.1.5")
		if err != nil {
			t.Fatalf("NewProxies() 报错: %v", err)
		}
		if !proxies.isTrusted(mustAddr(t, "10.1.2.3")) || !proxies.isTrusted(mustAddr(t, "192.168.1.5")) {
			t.Error("合法条目应当被信任")
		}
		if proxies.isTrusted(mustAddr(t, "192.168.1.6")) {
			t.Error("不在列表里的地址不该被信任")
		}
	})
	t.Run("写错的条目报错", func(t *testing.T) {
		for _, spec := range []string{"not-an-ip", "10.0.0.0/33", "999.1.1.1"} {
			if _, err := NewProxies(spec); err == nil {
				t.Errorf("NewProxies(%q) 应当报错", spec)
			}
		}
	})
}

func TestClientIP(t *testing.T) {
	defaultProxies, err := NewProxies("")
	if err != nil {
		t.Fatalf("NewProxies() 报错: %v", err)
	}
	wideProxies, err := NewProxies("127.0.0.1/32,10.0.0.0/8,::1/128")
	if err != nil {
		t.Fatalf("NewProxies() 报错: %v", err)
	}

	cases := []struct {
		name       string
		proxies    *Proxies
		remoteAddr string
		forwarded  string
		realIP     string
		want       string
	}{
		{name: "对端不可信就用对端", proxies: defaultProxies, remoteAddr: "8.8.8.8:4321", forwarded: "1.2.3.4", want: "8.8.8.8"},
		{name: "对端可信且无 XFF", proxies: defaultProxies, remoteAddr: "127.0.0.1:4321", want: "127.0.0.1"},
		{name: "单个 XFF", proxies: defaultProxies, remoteAddr: "127.0.0.1:4321", forwarded: "1.2.3.4", want: "1.2.3.4"},
		{name: "从右往左跳过可信 hop", proxies: wideProxies, remoteAddr: "127.0.0.1:4321", forwarded: "1.2.3.4, 10.1.2.3, 127.0.0.1", want: "1.2.3.4"},
		{name: "XFF 全可信时看 X-Real-IP", proxies: wideProxies, remoteAddr: "127.0.0.1:4321", forwarded: "10.2.3.4", realIP: "5.6.7.8", want: "5.6.7.8"},
		{name: "XFF 全可信且无 Real-IP 就用对端", proxies: wideProxies, remoteAddr: "127.0.0.1:4321", forwarded: "10.2.3.4", want: "127.0.0.1"},
		{name: "XFF 里的坏条目跳过", proxies: defaultProxies, remoteAddr: "127.0.0.1:4321", forwarded: "garbage, 1.2.3.4", want: "1.2.3.4"},
		{name: "IPv6 对端", proxies: defaultProxies, remoteAddr: "[::1]:4321", forwarded: "1.2.3.4", want: "1.2.3.4"},
		{name: "IPv6 对端无 XFF", proxies: defaultProxies, remoteAddr: "[::1]:4321", want: "::1"},
		{name: "XFF 里的 IPv4-mapped IPv6 归一", proxies: defaultProxies, remoteAddr: "127.0.0.1:4321", forwarded: "::ffff:1.2.3.4", want: "1.2.3.4"},
		{name: "坏 RemoteAddr 原样返回", proxies: defaultProxies, remoteAddr: "not-an-address", want: "not-an-address"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := item.proxies.ClientIP(request(item.remoteAddr, item.forwarded, item.realIP)); got != item.want {
				t.Errorf("ClientIP() = %q，想要 %q", got, item.want)
			}
		})
	}

	t.Run("nil Proxies 不 panic", func(t *testing.T) {
		var proxies *Proxies
		if got := proxies.ClientIP(request("1.2.3.4:5678", "9.9.9.9", "")); got != "1.2.3.4" {
			t.Errorf("ClientIP() = %q", got)
		}
	})
}

func request(remoteAddr, forwarded, realIP string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	return req
}

func mustAddr(t *testing.T, value string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("解析地址 %q 失败: %v", value, err)
	}
	return addr
}
