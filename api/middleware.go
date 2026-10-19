package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"file-share/app"
	"file-share/httpx"
)

// 三个通用闸门的默认槽位数：认证单独一组、其余接口一组、静态文件一组。
const (
	DefaultAuthConcurrency   = 32
	DefaultAPIConcurrency    = 128
	DefaultStaticConcurrency = 64
)

// ReadyzConcurrency 是 /readyz 的槽位数：它要碰库与文件，且只该由本机监控调用，固定几个就够。
const ReadyzConcurrency = 4

// 请求体上限：JSON 接口 64 KiB、登录 4 KiB、秒传预检 1 MiB；上传另外在
// MAX_UPLOAD_BYTES 之上留 1 MiB 给 multipart 的头与字段。
const (
	MaxJSONBodyBytes      = 64 << 10
	MaxAuthBodyBytes      = 4 << 10
	MaxCheckFileBodyBytes = 1 << 20
	uploadBodyOverhead    = 1 << 20
)

// ContentSecurityPolicy 是整站的 CSP：页面脚本全是构建期产出的外部文件（入口 HTML 没有
// 内联脚本），所以 script-src 只认 'self'；Vue 的 :style 绑定落在 style 属性上，
// 样式保留 'unsafe-inline' 这一档；其余全部收紧，并钉死 base-uri / form-action / frame-ancestors。
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none';"

// Concurrency 用固定容量的信号量把同时在处理的请求卡在 limit 以内；排队时客户端断开就直接放行，
// 不再占用槽位。
func Concurrency(limit int, next http.Handler) http.Handler {
	slots := make(chan struct{}, limit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		defer func() { <-slots }()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders 给所有响应补默认安全头：nosniff 挡 MIME 嗅探，no-referrer 不外泄地址，
// DENY 禁止被嵌入 iframe，CSP 把页面允许加载的资源限定在本站。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", ContentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// Logging 一条请求一行 JSON 访问日志。clientIP 由调用方给（可信代理规则见 httpx.Proxies）：
// 访问日志与审计用的是同一个字段名与同一个值。
//
// /assets/ 静态资源与 /livez 不记：一次页面加载就是十几个资源请求，存活探针又会被反复敲，
// 全是噪音；/readyz 照记 —— 它带 token 门槛，401/503 值得留痕。
func Logging(clientIP func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipAccessLog(r) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		recorder := httpx.NewStatusRecorder(w)
		next.ServeHTTP(recorder, r)

		level := slog.LevelInfo
		switch {
		case recorder.Status() >= 500:
			level = slog.LevelError
		case recorder.Status() >= 400:
			level = slog.LevelWarn
		}
		slog.Log(r.Context(), level, "HTTP Request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.Status()),
			slog.String("client_ip", clientIP(r)),
			slog.String("duration", time.Since(start).String()),
		)
	})
}

func skipAccessLog(r *http.Request) bool {
	path := r.URL.Path
	return strings.HasPrefix(path, "/assets/") || path == "/livez"
}

// LimitBody 按路径给请求体套上限：上限是每个接口的输入契约的一部分，集中在这里
// 而不是散在各个 handler 里。不认识的路径不包 —— 静态资源不读请求体。
func LimitBody(instance *app.App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, key, ok := bodyLimit(r.URL.Path, instance.Config.MaxUploadBytes)
		if ok && !limitBody(w, r, limit, key) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimit 给出一个路径的请求体上限与超限时的文案键；第二个返回值为 false 表示不限制。
func bodyLimit(path string, maxUploadBytes int64) (int64, string, bool) {
	switch path {
	case "/api/upload":
		return maxUploadBytes + uploadBodyOverhead, "upload.too-big", true
	case "/api/check_file":
		return MaxCheckFileBodyBytes, "request.too-big", true
	case "/api/auth":
		return MaxAuthBodyBytes, "request.too-big", true
	}
	if strings.HasPrefix(path, "/api/") {
		return MaxJSONBodyBytes, "request.too-big", true
	}
	return 0, "", false
}

// limitBody 先看 Content-Length（未知时是 -1），再给请求体套 MaxBytesReader；
// 超限当场回 413 与对应文案键。返回 false 表示已经应答，调用方直接返回。
func limitBody(w http.ResponseWriter, r *http.Request, limit int64, key string) bool {
	if r.ContentLength > limit {
		fail(w, r, http.StatusRequestEntityTooLarge, key)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

// isBodyTooLarge 判断读请求体时是不是撞上了 MaxBytesReader 的上限
// （Content-Length 缺失、靠分块传超大体的情况）。
func isBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}
