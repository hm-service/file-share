package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"file-share/core"
	"file-share/httpx"
	"file-share/i18n"
)

type langKey struct{}

// WithLanguage 按 Accept-Language 挑一档文案语言放进请求上下文，并回 Content-Language。
// Vary 提醒缓存：同一个 URL 换语言就是另一份响应。
func WithLanguage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.Parse(r.Header.Get("Accept-Language"))
		header := w.Header()
		header.Set("Content-Language", string(lang))
		header.Add("Vary", "Accept-Language")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), langKey{}, lang)))
	})
}

// language 取当前请求的文案语言；没经过中间件时就是源语言（简体中文）。
func language(r *http.Request) i18n.Lang {
	if lang, ok := r.Context().Value(langKey{}).(i18n.Lang); ok {
		return lang
	}
	return i18n.Default
}

// fail 写统一错误体 {"detail": "…"}，文案按请求语言取。
func fail(w http.ResponseWriter, r *http.Request, status int, key string) {
	httpx.Fail(w, status, i18n.T(language(r), key))
}

// unauthorized 是 401 的固定写法（带 WWW-Authenticate），文案同样本地化。
func unauthorized(w http.ResponseWriter, r *http.Request, key string) {
	httpx.Unauthorized(w, i18n.T(language(r), key))
}

// businessFail 把业务层抛的「状态码 + 文案键」渲染成当前语言的错误体；不是业务错误的一律 500。
func businessFail(w http.ResponseWriter, r *http.Request, err error) {
	var business *core.Error
	if errors.As(err, &business) {
		fail(w, r, business.Status, business.Key)
		return
	}
	slog.ErrorContext(r.Context(), "unhandled business error", slog.Any("error", err))
	fail(w, r, http.StatusInternalServerError, "internal")
}
