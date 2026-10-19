package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrencyCapsInFlightRequests(t *testing.T) {
	const limit = 4
	var inFlight, peak atomic.Int64
	handler := Concurrency(limit, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inFlight.Add(-1)
	}))

	var group sync.WaitGroup
	for range limit + 12 {
		group.Go(func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/files", nil))
		})
	}
	group.Wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("同时在处理的请求峰值 = %d，超过上限 %d", got, limit)
	} else if got < 2 {
		t.Fatalf("峰值只有 %d，并发没跑起来，这个用例说明不了问题", got)
	}
}

// 排队时客户端断开：直接放行，不占槽位也不调用后面的处理器。
func TestConcurrencyGivesUpWhenClientGoesAway(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	handler := Concurrency(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-block
	}))

	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/files", nil))
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/files", nil).WithContext(ctx)
	waiting := make(chan struct{})
	go func() {
		defer close(waiting)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}()
	cancel()
	close(block)
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("槽位空出来后排队请求应当立刻过")
	}
}

// 闸门不认路径：套在谁身上就卡谁。
func TestConcurrencyCountsEveryPathItWraps(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 2)
	handler := Concurrency(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-block
	}))

	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	<-entered

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	}()
	select {
	case <-done:
		t.Fatal("槽位被占着，第二个请求不该过")
	case <-time.After(100 * time.Millisecond):
	}

	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("槽位空出来后排队请求应当立刻过")
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	header := recorder.Header()
	cases := map[string]string{
		"X-Content-Type-Options":      "nosniff",
		"Referrer-Policy":             "no-referrer",
		"X-Frame-Options":             "DENY",
		"Content-Security-Policy":     ContentSecurityPolicy,
		"Access-Control-Allow-Origin": "",
	}
	for name, want := range cases {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q，想要 %q", name, got, want)
		}
	}
	if !strings.Contains(ContentSecurityPolicy, "default-src 'none'") {
		t.Errorf("CSP 应当以 default-src 'none' 打底: %q", ContentSecurityPolicy)
	}
}

func TestAllowValue(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		want    string
	}{
		{name: "GET 补 HEAD", methods: []string{http.MethodGet}, want: "GET, HEAD"},
		{name: "已含 HEAD", methods: []string{http.MethodGet, http.MethodHead}, want: "GET, HEAD"},
		{name: "只有 POST", methods: []string{http.MethodPost}, want: "POST"},
		{name: "DELETE", methods: []string{http.MethodDelete}, want: "DELETE"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := allowValue(item.methods); got != item.want {
				t.Errorf("allowValue(%v) = %q，想要 %q", item.methods, got, item.want)
			}
		})
	}
}

func TestSkipAccessLog(t *testing.T) {
	cases := []struct {
		method string
		target string
		skip   bool
	}{
		{method: http.MethodGet, target: "/assets/app-abc.js", skip: true},
		{method: http.MethodGet, target: "/assets/", skip: true},
		{method: http.MethodGet, target: "/livez", skip: true},
		{method: http.MethodHead, target: "/livez", skip: true},
		{method: http.MethodGet, target: "/readyz"},
		{method: http.MethodGet, target: "/api/files"},
		{method: http.MethodGet, target: "/favicon.svg"},
		{method: http.MethodPost, target: "/assets/app-abc.js", skip: true},
	}
	for _, item := range cases {
		t.Run(item.method+" "+item.target, func(t *testing.T) {
			request := httptest.NewRequest(item.method, item.target, nil)
			if got := skipAccessLog(request); got != item.skip {
				t.Errorf("skipAccessLog(%s %s) = %v，想要 %v", item.method, item.target, got, item.skip)
			}
		})
	}
}

func TestLimitBody(t *testing.T) {
	t.Run("Content-Length 超限", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limitBody(w, r, 4, "request.too-big") {
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789")))
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("状态码 = %d，想要 413", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "请求体过大") {
			t.Errorf("错误体 = %s", recorder.Body.String())
		}
	})

	t.Run("没有 Content-Length 时靠 MaxBytesReader 兜底", func(t *testing.T) {
		var readErr error
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limitBody(w, r, 4, "request.too-big") {
				return
			}
			_, readErr = io.ReadAll(r.Body)
		})
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789"))
		request.ContentLength = -1
		request.Header.Del("Content-Length")
		handler.ServeHTTP(httptest.NewRecorder(), request)
		if !isBodyTooLarge(readErr) {
			t.Fatalf("读超限请求体应当报 MaxBytesError，得到 %v", readErr)
		}
	})

	t.Run("限额内正常放行", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limitBody(w, r, 64, "request.too-big") {
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "hello" {
				t.Errorf("body = %q err=%v", body, err)
			}
		})
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello")))
	})
}

func TestBodyLimit(t *testing.T) {
	const maxUpload = int64(1 << 20)
	cases := []struct {
		name    string
		path    string
		limit   int64
		key     string
		limited bool
	}{
		{name: "登录 4 KiB", path: "/api/auth", limit: MaxAuthBodyBytes, key: "request.too-big", limited: true},
		{name: "秒传预检 1 MiB", path: "/api/check_file", limit: MaxCheckFileBodyBytes, key: "request.too-big", limited: true},
		{name: "上传 MAX + 1 MiB", path: "/api/upload", limit: maxUpload + uploadBodyOverhead, key: "upload.too-big", limited: true},
		{name: "其它 JSON 接口 64 KiB", path: "/api/files", limit: MaxJSONBodyBytes, key: "request.too-big", limited: true},
		{name: "带路径参数的接口", path: "/api/files/abc/favorite", limit: MaxJSONBodyBytes, key: "request.too-big", limited: true},
		{name: "页面不限制", path: "/"},
		{name: "静态资源不限制", path: "/assets/app-abc.js"},
		{name: "探针不限制", path: "/readyz"},
		{name: "公开下载不限制", path: "/s/abc"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			limit, key, limited := bodyLimit(item.path, maxUpload)
			if limited != item.limited {
				t.Fatalf("bodyLimit(%q) limited = %v，想要 %v", item.path, limited, item.limited)
			}
			if limited && (limit != item.limit || key != item.key) {
				t.Errorf("bodyLimit(%q) = %d %q，想要 %d %q", item.path, limit, key, item.limit, item.key)
			}
		})
	}
}
