package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"file-share/core"
	"file-share/i18n"
)

// WithLanguage 按 Accept-Language 挑语言，并把 Content-Language / Vary 一并写出去。
func TestWithLanguage(t *testing.T) {
	handler := WithLanguage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Lang", string(language(r)))
	}))
	cases := []struct {
		name     string
		header   string
		wantLang i18n.Lang
	}{
		{name: "默认中文", header: "", wantLang: i18n.ZhHans},
		{name: "英文", header: "en-US,en;q=0.9", wantLang: i18n.En},
		{name: "中文", header: "zh-CN", wantLang: i18n.ZhHans},
		{name: "认不出用默认", header: "fr-FR", wantLang: i18n.ZhHans},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if item.header != "" {
				request.Header.Set("Accept-Language", item.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if got := recorder.Header().Get("Content-Language"); got != string(item.wantLang) {
				t.Errorf("Content-Language = %q，想要 %q", got, item.wantLang)
			}
			if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Accept-Language") {
				t.Errorf("Vary = %q，应当包含 Accept-Language", got)
			}
			if got := recorder.Header().Get("X-Test-Lang"); got != string(item.wantLang) {
				t.Errorf("上下文里的语言 = %q，想要 %q", got, item.wantLang)
			}
		})
	}
}

// 不在中间件后面时 language() 兜底为源语言，不会拿到空值。
func TestLanguageWithoutMiddleware(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := language(request); got != i18n.Default {
		t.Errorf("language() = %q，想要默认 %q", got, i18n.Default)
	}
}

func TestFailLocalized(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "中文", header: "", want: "找不到该文件"},
		{name: "英文", header: "en", want: "File not found"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			handler := WithLanguage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fail(w, r, http.StatusNotFound, "file.not-found")
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if item.header != "" {
				request.Header.Set("Accept-Language", item.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Errorf("状态码 = %d", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), item.want) {
				t.Errorf("错误体 = %s，想要包含 %q", recorder.Body.String(), item.want)
			}
		})
	}
}

func TestBusinessFail(t *testing.T) {
	t.Run("业务错误按状态与键渲染", func(t *testing.T) {
		handler := WithLanguage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			businessFail(w, r, core.Fail(413, "upload.too-big"))
		}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("状态码 = %d，想要 413", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "文件超过上传上限") {
			t.Errorf("错误体 = %s", recorder.Body.String())
		}
	})

	t.Run("普通错误一律 500", func(t *testing.T) {
		handler := WithLanguage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			businessFail(w, r, http.ErrAbortHandler)
		}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("状态码 = %d，想要 500", recorder.Code)
		}
	})
}
