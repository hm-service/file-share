package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"file-share/api"
	"file-share/app"
	"file-share/internal/testenv"
)

func newDav(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	instance := testenv.New(t)
	return instance, api.DavHandler(instance)
}

// davDo 发一个 WebDAV 请求（默认带 Basic 凭据）。
func davDo(handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.SetBasicAuth("anyone", testenv.AuthToken)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("状态码 = %d，想要 %d（body=%s）", recorder.Code, want, recorder.Body.String())
	}
}

func TestDavRequiresBasicAuth(t *testing.T) {
	_, handler := newDav(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodOptions, "/dav/", nil))
	assertStatus(t, recorder, http.StatusUnauthorized)
	if challenge := recorder.Header().Get("WWW-Authenticate"); !strings.HasPrefix(challenge, "Basic ") {
		t.Errorf("WWW-Authenticate = %q，想要 Basic 挑战", challenge)
	}

	request := httptest.NewRequest(http.MethodOptions, "/dav/", nil)
	request.SetBasicAuth("anyone", "wrong-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertStatus(t, recorder, http.StatusUnauthorized)

	recorder = davDo(handler, http.MethodOptions, "/dav/", "", nil)
	assertStatus(t, recorder, http.StatusOK)
	if got := recorder.Header().Get("DAV"); got == "" {
		t.Error("OPTIONS 响应应当带 DAV 头")
	}
}

func TestDavFileLifecycle(t *testing.T) {
	instance, handler := newDav(t)
	content := "hello webdav 内容"

	assertStatus(t, davDo(handler, "MKCOL", "/dav/docs", "", nil), http.StatusCreated)
	assertStatus(t, davDo(handler, "PUT", "/dav/docs/a.txt", content, nil), http.StatusCreated)

	// 读回来：内容、长度、ETag（内容 sha256）都对。
	get := davDo(handler, http.MethodGet, "/dav/docs/a.txt", "", nil)
	assertStatus(t, get, http.StatusOK)
	if body, _ := io.ReadAll(get.Result().Body); string(body) != content {
		t.Errorf("GET 内容 = %q，想要 %q", body, content)
	}
	wantETag := `"` + testenv.SHA256([]byte(content)) + `"`
	if got := get.Header().Get("ETag"); got != wantETag {
		t.Errorf("ETag = %q，想要 %q", got, wantETag)
	}

	// Range 由 http.ServeContent 负责。
	rangeGet := davDo(handler, http.MethodGet, "/dav/docs/a.txt", "", map[string]string{"Range": "bytes=0-4"})
	assertStatus(t, rangeGet, http.StatusPartialContent)
	if body, _ := io.ReadAll(rangeGet.Result().Body); string(body) != content[:5] {
		t.Errorf("Range 内容 = %q", body)
	}

	// PROPFIND 深度 1 列出目录内容。
	propfind := davDo(handler, "PROPFIND", "/dav/docs", "", map[string]string{"Depth": "1"})
	assertStatus(t, propfind, http.StatusMultiStatus)
	body := propfind.Body.String()
	if !strings.Contains(body, "a.txt") || !strings.Contains(body, "getcontentlength") {
		t.Errorf("PROPFIND 响应缺少文件条目或长度属性: %s", body)
	}

	// MOVE 改名。
	moved := davDo(handler, "MOVE", "/dav/docs/a.txt", "", map[string]string{"Destination": "/dav/docs/b.txt"})
	assertStatus(t, moved, http.StatusCreated)
	assertStatus(t, davDo(handler, http.MethodGet, "/dav/docs/a.txt", "", nil), http.StatusNotFound)
	assertStatus(t, davDo(handler, http.MethodGet, "/dav/docs/b.txt", "", nil), http.StatusOK)

	// 目录递归删除：子文件一起消失。
	assertStatus(t, davDo(handler, "DELETE", "/dav/docs", "", nil), http.StatusNoContent)
	assertStatus(t, davDo(handler, "PROPFIND", "/dav/docs", "", map[string]string{"Depth": "0"}), http.StatusNotFound)

	// 同名覆盖：只有一条记录，内容换新。
	assertStatus(t, davDo(handler, "PUT", "/dav/replace.txt", "one", nil), http.StatusCreated)
	assertStatus(t, davDo(handler, "PUT", "/dav/replace.txt", "two", nil), http.StatusCreated)
	entries, err := instance.DavList("")
	if err != nil || len(entries) != 1 || entries[0].Name != "replace.txt" || entries[0].Size != 3 {
		t.Fatalf("覆盖后目录 = %+v, %v", entries, err)
	}
}

func TestDavErrors(t *testing.T) {
	instance, handler := newDav(t)

	// 父目录不存在：PUT 按冲突处理（409）。
	assertStatus(t, davDo(handler, "PUT", "/dav/missing/a.txt", "x", nil), http.StatusConflict)
	// 目标已存在时 MKCOL 是 405。
	assertStatus(t, davDo(handler, "MKCOL", "/dav/dir", "", nil), http.StatusCreated)
	assertStatus(t, davDo(handler, "MKCOL", "/dav/dir", "", nil), http.StatusMethodNotAllowed)
	// 不存在的资源。
	assertStatus(t, davDo(handler, http.MethodGet, "/dav/none.txt", "", nil), http.StatusNotFound)
	assertStatus(t, davDo(handler, "DELETE", "/dav/none.txt", "", nil), http.StatusNotFound)
	// 超过 MAX_FILE_SIZE：请求体上限直接拒绝。
	oversized := strings.Repeat("a", int(instance.Config.MaxFileSize)+1)
	assertStatus(t, davDo(handler, "PUT", "/dav/big.bin", oversized, nil), http.StatusRequestEntityTooLarge)
	if _, err := instance.DavStat("big.bin"); err == nil {
		t.Fatal("超限的上传不应留下记录")
	}
	// 非法路径段。
	assertStatus(t, davDo(handler, "MKCOL", "/dav/a..b", "", nil), http.StatusCreated)
	assertStatus(t, davDo(handler, "PUT", "/dav/a..b/x.txt", "x", nil), http.StatusCreated)
}

func TestDavTicketDownload(t *testing.T) {
	instance, handler := newDav(t)
	assertStatus(t, davDo(handler, "PUT", "/dav/a.txt", "ticket 内容", nil), http.StatusCreated)

	ticket, err := instance.IssueDavDownloadTicket("a.txt")
	if err != nil {
		t.Fatalf("IssueDavDownloadTicket() 报错: %v", err)
	}
	download := api.DavDownloadHandler(instance)

	recorder := httptest.NewRecorder()
	download.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/davdl?t="+ticket, nil))
	assertStatus(t, recorder, http.StatusOK)
	if recorder.Body.String() != "ticket 内容" {
		t.Errorf("票据下载内容 = %q", recorder.Body.String())
	}
	if disposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(disposition, "a.txt") {
		t.Errorf("Content-Disposition = %q，应当带文件名", disposition)
	}

	// 篡改票据：一律 404，不区分是过期还是伪造。
	bad := httptest.NewRecorder()
	download.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/davdl?t="+ticket+"x", nil))
	assertStatus(t, bad, http.StatusNotFound)
}