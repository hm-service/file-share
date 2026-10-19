package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"file-share/app"
	"file-share/core"
	"file-share/httpx"
)

type Server struct {
	App *app.App
}

// NewHandler 是只含接口的完整处理器，供测试或只关心接口的调用方使用；
// main.go 会把同一棵路由树按前缀套上各自的环境变量闸门。
func NewHandler(instance *app.App) http.Handler {
	return Middleware(instance, Concurrency(DefaultAPIConcurrency, NewMux(instance)))
}

// Middleware 给任意处理器套上站点通用中间件：访问日志 + 安全响应头 + 语言协商 + 请求体上限。
// 并发闸门不在这里：接口、认证、静态各是独立信号量，由接线处各套一层。
func Middleware(instance *app.App, next http.Handler) http.Handler {
	return Logging(instance.ClientIP, SecurityHeaders(WithLanguage(LimitBody(instance, next))))
}

type route struct {
	method    string
	pattern   string
	handler   http.HandlerFunc
	protected bool
}

// NewMux 挂载 /api/*、/s/* 与探针。页面与静态资源不在这里（那是根目录 web.go 的事），
// main.go 负责把两者接到一棵路由树上。
func NewMux(instance *app.App) *http.ServeMux {
	server := &Server{App: instance}
	mux := http.NewServeMux()
	server.register(mux, []route{
		{http.MethodPost, "/api/auth", server.handleAuth, false},
		{http.MethodGet, "/livez", server.handleLivez, false},
		{http.MethodGet, "/readyz", server.handleReadyz, false},
		{http.MethodGet, "/api/files", server.handleListFiles, true},
		{http.MethodPost, "/api/check_file", server.handleCheckFile, true},
		{http.MethodPost, "/api/upload", server.handleUpload, true},
		{http.MethodPost, "/api/files/{file_id}/favorite", server.handleFavorite, true},
		{http.MethodPost, "/api/files/{file_id}/public", server.handlePublic, true},
		{http.MethodGet, "/api/files/{file_id}/download", server.handleDownload, true},
		{http.MethodDelete, "/api/files/{file_id}", server.handleDelete, true},
		{http.MethodGet, "/s/{file_id}", server.handlePublicDownload, false},
	})
	mux.HandleFunc("/", server.handleNotFound)
	return mux
}

// register 按「METHOD 路径」注册，并给每条路径补一条不限方法的规则用来回 405。
//
// 不靠 ServeMux 自己回 405：它只在没有任何模式匹配时才这么做，而底部那条 / 兜底匹配一切，
// 方法不对就会被兜成 404。补上路径规则后，路径匹配、方法不匹配就落到这里，带上 Allow 回 405。
func (s *Server) register(mux *http.ServeMux, routes []route) {
	allowed := make(map[string][]string, len(routes))
	for _, item := range routes {
		handler := item.handler
		if item.protected {
			handler = s.requireSession(handler)
		}
		allowed[item.pattern] = append(allowed[item.pattern], item.method)
		mux.HandleFunc(item.method+" "+item.pattern, handler)
	}
	for pattern, methods := range allowed {
		allow := allowValue(methods)
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			// /api/* 与 /s/* 是给程序用的接口，405 也要是 JSON 错误体。
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/s/") {
				fail(w, r, http.StatusMethodNotAllowed, "method-not-allowed")
				return
			}
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		})
	}
}

// allowValue 拼 Allow 头。Go 的 "GET" 模式同时也接 HEAD，所以 GET 要连 HEAD 一起写出来。
func allowValue(methods []string) string {
	if slices.Contains(methods, http.MethodGet) && !slices.Contains(methods, http.MethodHead) {
		methods = append(methods, http.MethodHead)
	}
	return strings.Join(methods, ", ")
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	switch path := r.URL.Path; {
	case strings.HasPrefix(path, "/api/"):
		fail(w, r, http.StatusNotFound, "not-found")
	case strings.HasPrefix(path, "/s/"):
		// 公开路由上的一切「没有」都回同一句话，不泄露记录是否存在 / 是否私有。
		fail(w, r, http.StatusNotFound, "file.not-found")
	default:
		http.NotFound(w, r)
	}
}

// handleAuth 校验 AUTH_TOKEN 并签发会话凭据。整个请求的最短耗时由 app.Authenticate 保证。
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		if isBodyTooLarge(err) {
			fail(w, r, http.StatusRequestEntityTooLarge, "request.too-big")
			return
		}
		fail(w, r, http.StatusBadRequest, "request.json")
		return
	}
	if !s.App.Authenticate(payload.Token) {
		unauthorized(w, r, "auth.invalid")
		return
	}
	token, expiresAt, err := s.App.IssueSession()
	if err != nil {
		slog.ErrorContext(r.Context(), "issue session failed", slog.Any("error", err))
		fail(w, r, http.StatusInternalServerError, "session.issue")
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"token": token, "expires_at": expiresAt})
}

// requireSession 是 /api/*（登录除外）的身份闸门：缺凭据、格式错、过期分别给不同文案键。
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			unauthorized(w, r, "auth.required")
			return
		}
		err := s.App.VerifySession(token)
		switch {
		case err == nil:
		case errors.Is(err, core.ErrSessionExpired):
			unauthorized(w, r, "auth.expired")
			return
		default:
			unauthorized(w, r, "auth.malformed")
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
