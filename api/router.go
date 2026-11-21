package api

import (
	"context"
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
	// permission 是访问该接口所需权限；public 路由不看它。
	permission app.Permission
}

// NewMux 挂载 /api/* 与探针。页面与静态资源不在这里（那是根目录 web.go 的事），
// main.go 负责把两者接到一棵路由树上。
func NewMux(instance *app.App) *http.ServeMux {
	server := &Server{App: instance}
	mux := http.NewServeMux()
	server.register(mux, []route{
		{http.MethodPost, "/api/auth", server.handleAuth, false, 0},
		{http.MethodGet, "/livez", server.handleLivez, false, 0},
		{http.MethodGet, "/readyz", server.handleReadyz, false, 0},
		{http.MethodGet, "/api/me", server.handleMe, true, app.PermissionRead},
		{http.MethodGet, "/api/files", server.handleListFiles, true, app.PermissionRead},
		{http.MethodGet, "/api/storage", server.handleStorage, true, app.PermissionRead},
		{http.MethodPost, "/api/check_file", server.handleCheckFile, true, app.PermissionWrite},
		{http.MethodPost, "/api/upload", server.handleUpload, true, app.PermissionWrite},
		{http.MethodPost, "/api/files/{file_id}/favorite", server.handleFavorite, true, app.PermissionWrite},
		{http.MethodPost, "/api/files/{file_id}/share", server.handleShareLink, true, app.PermissionRead},
		{http.MethodGet, "/api/files/{file_id}/download", server.handleDownload, true, app.PermissionRead},
		{http.MethodGet, "/api/files/{file_id}/download-url", server.handleDownloadURL, true, app.PermissionRead},
		{http.MethodPost, "/api/groups", server.handleCreateGroup, true, app.PermissionWrite},
		{http.MethodPost, "/api/groups/rename", server.handleRenameGroup, true, app.PermissionWrite},
		{http.MethodPost, "/api/groups/delete", server.handleDeleteGroup, true, app.PermissionWrite},
		{http.MethodPut, "/api/groups/order", server.handleSetGroupOrder, true, app.PermissionWrite},
		{http.MethodPatch, "/api/files/{file_id}", server.handleUpdateFile, true, app.PermissionWrite},
		{http.MethodDelete, "/api/files/{file_id}", server.handleDelete, true, app.PermissionWrite},
		// /s/{file_id}?sig=&exp= 是浏览器导航下载与分享链接入口：签名绑定单个文件，
		// 不需要 Authorization。
		{http.MethodGet, "/s/{file_id}", server.handleSignedDownload, false, 0},
	})
	// /api/files/order 是字面量路径，不能再走 register 的「同路径补一条不限方法」兜底：
	// 那条兜底会跟 /api/files/{file_id} 的方法模式冲突，ServeMux 注册时就 panic。
	// 与通配符方法重叠的 PATCH / DELETE 会命中真实的文件处理器，必须显式拦下来；
	// 其余方法落到通配符兜底，靠 literalRoutes 给出正确的 Allow。
	mux.HandleFunc(http.MethodPut+" "+orderPath, server.requireSession(app.PermissionWrite, server.handleSetOrder))
	mux.HandleFunc(http.MethodPatch+" "+orderPath, server.rejectOrderMethod)
	mux.HandleFunc(http.MethodDelete+" "+orderPath, server.rejectOrderMethod)
	mux.HandleFunc("/", server.handleNotFound)
	return mux
}

// orderPath 是批量排序接口的字面量路径：它是 /api/files/{file_id} 下的保留字。
const orderPath = "/api/files/order"

// literalRoutes 把字面量路径映射到它真正支持的方法：这类路径不会命中通配符的
// 405 兜底（兜底的 Allow 是通配符自己的方法集合），要单独换算。
var literalRoutes = map[string]string{orderPath: http.MethodPut}

// rejectOrderMethod 回 405 并声明 /api/files/order 只支持 PUT。
func (s *Server) rejectOrderMethod(w http.ResponseWriter, r *http.Request) {
	s.methodNotAllowed(w, r, http.MethodPut)
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
			handler = s.requireSession(item.permission, handler)
		}
		allowed[item.pattern] = append(allowed[item.pattern], item.method)
		mux.HandleFunc(item.method+" "+item.pattern, handler)
	}
	for pattern, methods := range allowed {
		allow := allowValue(methods)
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			// 字面量路径实际支持的方法与通配符不同，按路径换算 Allow。
			// 不能改写闭包里的 allow：同一处理器还会服务通配符路径的请求。
			header := allow
			if literal, ok := literalRoutes[r.URL.Path]; ok {
				header = literal
			}
			s.methodNotAllowed(w, r, header)
		})
	}
}

// methodNotAllowed 回 405 并带上该路径支持的 Allow 头。
// /api/* 与 /s* 是给程序 / 浏览器下载用的接口，405 也要是 JSON 错误体；其余保持纯文本。
func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	if strings.HasPrefix(r.URL.Path, "/api/") ||
		r.URL.Path == "/s" || strings.HasPrefix(r.URL.Path, "/s/") {
		fail(w, r, http.StatusMethodNotAllowed, "method-not-allowed")
		return
	}
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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
	case path == "/s" || strings.HasPrefix(path, "/s/"):
		// 下载入口只认 /s/{file_id}?sig=&exp=，其余一律按「链接无效」处理。
		fail(w, r, http.StatusNotFound, "download.invalid")
	default:
		http.NotFound(w, r)
	}
}

// handleAuth 校验用户名与密码并签发会话凭据。整个请求的最短耗时由 app.Authenticate 保证。
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if !s.App.Authenticate(strings.TrimSpace(payload.Username), payload.Password) {
		unauthorized(w, r, "auth.invalid")
		return
	}
	username := strings.TrimSpace(payload.Username)
	token, expiresAt, err := s.App.IssueSession(username)
	if err != nil {
		slog.ErrorContext(r.Context(), "issue session failed", slog.Any("error", err))
		fail(w, r, http.StatusInternalServerError, "session.issue")
		return
	}
	user, _ := s.App.User(username)
	httpx.Write(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": expiresAt,
		"user":       userPayload(user),
	})
}

// handleMe 返回当前登录用户与权限，供界面决定显示哪些操作。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	httpx.Write(w, http.StatusOK, userPayload(currentUser(r)))
}

// userPayload 是给界面的用户信息：只暴露名字与权限，永远不带密码。
func userPayload(user app.User) map[string]any {
	return map[string]any{"name": user.Name, "permissions": user.Permissions.Names()}
}

// userContextKey 是请求上下文里放当前用户的键（包内私有类型，别的包塞不进来）。
type userContextKey struct{}

// currentUser 取 requireSession 放进上下文的当前用户；只有经过身份闸门的处理函数能拿到。
func currentUser(r *http.Request) app.User {
	user, _ := r.Context().Value(userContextKey{}).(app.User)
	return user
}

// requireSession 是 /api/*（登录除外）的身份与权限闸门：
// 缺凭据 / 格式错 / 过期分别给不同文案键，凭据有效但缺所需权限回 403。
func (s *Server) requireSession(permission app.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			unauthorized(w, r, "auth.required")
			return
		}
		user, err := s.App.VerifySession(token)
		switch {
		case err == nil:
		case errors.Is(err, core.ErrSessionExpired):
			unauthorized(w, r, "auth.expired")
			return
		default:
			unauthorized(w, r, "auth.malformed")
			return
		}
		if !user.Can(permission) {
			fail(w, r, http.StatusForbidden, "permission.denied")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
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
