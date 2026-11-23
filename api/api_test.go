package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"file-share/app"
	"file-share/internal/testenv"
)

func newHandler(t *testing.T) (*app.App, http.Handler, string) {
	t.Helper()
	instance := testenv.New(t)
	handler := NewHandler(instance)
	return instance, handler, testenv.Token(t, instance)
}

type filePayload struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	GroupPath  string `json:"group_path"`
	Size       int64  `json:"size"`
	CreatedAt  int64  `json:"created_at"`
	IsFavorite bool   `json:"is_favorite"`
}

type groupPayload struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

type listPayload struct {
	Files  []filePayload  `json:"files"`
	Groups []groupPayload `json:"groups"`
}

func listOf(t *testing.T, handler http.Handler, token string) listPayload {
	t.Helper()
	recorder := testenv.Do(handler, http.MethodGet, "/api/files", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/files = %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload listPayload
	testenv.DecodeInto(t, recorder, &payload)
	return payload
}

func TestRequiresAuth(t *testing.T) {
	_, handler, _ := newHandler(t)
	for _, target := range []string{"/api/files", "/api/me", "/api/storage"} {
		recorder := testenv.Do(handler, http.MethodGet, target, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s = %d，想要 401", target, recorder.Code)
		}
	}
	if recorder := testenv.Do(handler, http.MethodGet, "/api/files", "bad-token"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("坏 token = %d，想要 401", recorder.Code)
	}
}

func TestLoginAndMe(t *testing.T) {
	_, handler, _ := newHandler(t)
	token, expiresAt := testenv.Login(t, handler)
	if expiresAt <= time.Now().Unix() {
		t.Fatalf("会话过期时间 = %d，应当在未来", expiresAt)
	}
	recorder := testenv.Do(handler, http.MethodGet, "/api/me", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/me = %d", recorder.Code)
	}
	payload := testenv.Decode(t, recorder)
	if payload["name"] != testenv.UserName {
		t.Errorf("name = %v，想要 %q", payload["name"], testenv.UserName)
	}
	permissions, _ := payload["permissions"].([]any)
	if len(permissions) != 2 || permissions[0] != "read" || permissions[1] != "fav" {
		t.Errorf("permissions = %v，想要 [read fav]", permissions)
	}
	access, _ := payload["access"].([]any)
	if len(access) != 1 || access[0] != "/" {
		t.Errorf("access = %v，想要 [/]", access)
	}
}

// TestFavoriteRequiresFavPermission 验证收藏接口按 fav 权限放行：只有 read 的账号拿到 403，
// 但浏览与下载不受影响。
func TestFavoriteRequiresFavPermission(t *testing.T) {
	instance := testenv.NewWith(t, nil, func(cfg *app.Config) {
		cfg.Users = append(cfg.Users, app.User{
			Name:        "guest",
			Password:    "guest_password_0123456789",
			Permissions: app.PermissionRead,
			Access:      []string{"/"},
		})
	})
	handler := NewHandler(instance)
	guestToken := testenv.TokenFor(t, instance, "guest")

	if recorder := testenv.Do(handler, http.MethodGet, "/api/files", guestToken); recorder.Code != http.StatusOK {
		t.Fatalf("无 fav 权限的浏览 = %d，想要 200", recorder.Code)
	}
	recorder := testenv.Do(handler, http.MethodPut,
		"/api/favorites?path="+queryEscape("软件/说明.md"), guestToken)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("无 fav 权限收藏 = %d，想要 403: %s", recorder.Code, recorder.Body.String())
	}
}

func TestListFiles(t *testing.T) {
	_, handler, token := newHandler(t)
	payload := listOf(t, handler, token)
	if len(payload.Files) != len(testenv.DefaultFiles()) {
		t.Fatalf("文件数 = %d，想要 %d", len(payload.Files), len(testenv.DefaultFiles()))
	}
	byID := make(map[string]filePayload, len(payload.Files))
	for _, file := range payload.Files {
		byID[file.ID] = file
	}
	app, ok := byID["软件/安卓应用/app.apk"]
	if !ok {
		t.Fatalf("缺少文件记录 software/安卓应用/app.apk：%+v", byID)
	}
	if app.Filename != "app.apk" || app.GroupPath != "软件/安卓应用" {
		t.Errorf("记录字段不对: %+v", app)
	}
	if app.Size != int64(len("apk-content")) {
		t.Errorf("size = %d，想要 %d", app.Size, len("apk-content"))
	}
	if app.IsFavorite {
		t.Error("新实例的文件不应当是收藏状态")
	}
	if app.CreatedAt <= 0 {
		t.Errorf("created_at = %d，应当是正数", app.CreatedAt)
	}
	// 空目录也要出现在分组列表里。
	groups := make(map[string]groupPayload, len(payload.Groups))
	for _, group := range payload.Groups {
		groups[group.Path] = group
	}
	if _, ok := groups["空目录"]; !ok {
		t.Errorf("缺少空目录分组: %+v", groups)
	}
	if group := groups["软件/安卓应用"]; group.Parent != "软件" || group.Name != "安卓应用" {
		t.Errorf("分组字段不对: %+v", group)
	}
}

// TestListFilesIncludesHidden 验证点开头的文件与目录是普通数据：照常出现在列表里，
// 只有符号链接这类非常规文件被跳过。
func TestListFilesIncludesHidden(t *testing.T) {
	instance := testenv.NewWith(t, map[string]string{
		"可见.txt":                     "visible",
		".隐藏文件.txt":                  "hidden",
		".file-share/manifest.jsonl": "{}",
		"子目录/内部.txt":                 "inner",
	}, nil)
	handler := NewHandler(instance)
	token := testenv.Token(t, instance)
	recorder := testenv.Do(handler, http.MethodGet, "/api/files", token)
	var payload listPayload
	testenv.DecodeInto(t, recorder, &payload)
	if len(payload.Files) != 4 {
		t.Fatalf("文件数 = %d，想要 4（隐藏文件也展示）: %+v", len(payload.Files), payload.Files)
	}
	found := map[string]bool{}
	for _, file := range payload.Files {
		found[file.ID] = true
	}
	if !found[".隐藏文件.txt"] || !found[".file-share/manifest.jsonl"] {
		t.Errorf("隐藏文件与隐藏目录下的文件应当出现: %v", found)
	}
}

func TestFavoriteToggle(t *testing.T) {
	instance, handler, token := newHandler(t)
	target := "/api/favorites?path=" + queryEscape("软件/说明.md")

	recorder := testenv.Do(handler, http.MethodPut, target, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT 收藏 = %d: %s", recorder.Code, recorder.Body.String())
	}
	if payload := testenv.Decode(t, recorder); payload["is_favorite"] != true {
		t.Fatalf("is_favorite = %v，想要 true", payload["is_favorite"])
	}
	for _, file := range listOf(t, handler, token).Files {
		if file.ID == "软件/说明.md" && !file.IsFavorite {
			t.Fatal("列表里应当显示为已收藏")
		}
	}
	// 收藏元数据落在 DATA_DIR/favorites/<user>.json 下，而不是只读数据目录。
	if _, err := os.Stat(filepath.Join(instance.Config.DataDir, "favorites", testenv.UserName+".json")); err != nil {
		t.Fatalf("用户收藏文件不存在: %v", err)
	}

	recorder = testenv.Do(handler, http.MethodDelete, target, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("DELETE 取消收藏 = %d", recorder.Code)
	}
	if payload := testenv.Decode(t, recorder); payload["is_favorite"] != false {
		t.Fatalf("is_favorite = %v，想要 false", payload["is_favorite"])
	}
}

func TestFavoriteRejectsBadPath(t *testing.T) {
	_, handler, token := newHandler(t)
	// 点开头的路径是普通数据，只有穿越与空路径被拒绝。
	for _, path := range []string{"", "../etc/passwd", "/abs", "软件\\说明.md"} {
		recorder := testenv.Do(handler, http.MethodPut, "/api/favorites?path="+queryEscape(path), token)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("收藏路径 %q = %d，想要 400", path, recorder.Code)
		}
	}
}

func TestShareLinkAndSignedDownload(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/share",
		map[string]any{"path": "软件/说明.md", "ttl": "10m"}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/share = %d: %s", recorder.Code, recorder.Body.String())
	}
	payload := testenv.Decode(t, recorder)
	link, _ := payload["url"].(string)
	if !strings.HasPrefix(link, "/s/") {
		t.Fatalf("分享链接 = %q，想要 /s/ 前缀", link)
	}
	// 分享链接不需要 Authorization。
	download := testenv.Do(handler, http.MethodGet, link, "")
	if download.Code != http.StatusOK {
		t.Fatalf("签名下载 = %d: %s", download.Code, download.Body.String())
	}
	if got := download.Body.String(); got != "# 说明\n" {
		t.Errorf("下载内容 = %q", got)
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Errorf("Content-Disposition = %q", disposition)
	}
}

func TestShareLink24h(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/share",
		map[string]any{"path": "根目录文件.txt", "ttl": "24h"}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("24h 分享 = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestShareLinkRejectsBadTTL(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/share",
		map[string]any{"path": "根目录文件.txt", "ttl": "5y"}, token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法 ttl = %d，想要 400", recorder.Code)
	}
}

func TestDownloadURL(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.Do(handler, http.MethodGet, "/api/download-url?path="+queryEscape("我的世界/地图/map.zip"), token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/download-url = %d: %s", recorder.Code, recorder.Body.String())
	}
	link, _ := testenv.Decode(t, recorder)["url"].(string)
	download := testenv.Do(handler, http.MethodGet, link, "")
	if download.Code != http.StatusOK || download.Body.String() != "map-content" {
		t.Fatalf("下载 = %d %q", download.Code, download.Body.String())
	}
}

func TestSignedDownloadRejectsTamperedSignature(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/share",
		map[string]any{"path": "软件/说明.md", "ttl": "10m"}, token)
	link, _ := testenv.Decode(t, recorder)["url"].(string)
	tampered := strings.Replace(link, "sig=", "sig=x", 1)
	if got := testenv.Do(handler, http.MethodGet, tampered, "").Code; got != http.StatusForbidden {
		t.Fatalf("坏签名 = %d，想要 403", got)
	}
	if got := testenv.Do(handler, http.MethodGet, "/s/软件/说明.md", "").Code; got != http.StatusNotFound {
		t.Fatalf("没有签名 = %d，想要 404", got)
	}
}

func TestMissingFile(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.Do(handler, http.MethodGet, "/api/download-url?path="+queryEscape("不存在.txt"), token)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的文件 = %d，想要 404: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPathTraversalRejected(t *testing.T) {
	_, handler, token := newHandler(t)
	for _, path := range []string{"../etc/passwd", "/etc/passwd", "软件/../../etc/passwd", "软件\\说明.md"} {
		recorder := testenv.Do(handler, http.MethodGet, "/api/download-url?path="+queryEscape(path), token)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("路径 %q = %d，想要 400", path, recorder.Code)
		}
	}
}

func TestUnknownRoute(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.Do(handler, http.MethodGet, "/api/nope", token)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知接口 = %d，想要 404", recorder.Code)
	}
	if payload := testenv.Decode(t, recorder); payload["detail"] == nil {
		t.Errorf("错误体缺少 detail: %s", recorder.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.Do(handler, http.MethodGet, "/api/share", token)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/share = %d，想要 405", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); !strings.Contains(allow, "POST") {
		t.Errorf("Allow = %q", allow)
	}
}

func TestStorage(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.Do(handler, http.MethodGet, "/api/storage", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/storage = %d", recorder.Code)
	}
	payload := testenv.Decode(t, recorder)
	if payload["backend"] != "dir" || payload["healthy"] != true {
		t.Errorf("storage = %v，想要 backend=dir healthy=true", payload)
	}
}

func TestUnicodePathRoundTrip(t *testing.T) {
	_, handler, token := newHandler(t)
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/share",
		map[string]any{"path": "我的世界/地图/map.zip", "ttl": "10m"}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("中文路径分享 = %d: %s", recorder.Code, recorder.Body.String())
	}
	link, _ := testenv.Decode(t, recorder)["url"].(string)
	download := testenv.Do(handler, http.MethodGet, link, "")
	if download.Code != http.StatusOK || download.Body.String() != "map-content" {
		t.Fatalf("中文路径下载 = %d %q", download.Code, download.Body.String())
	}
}

// queryEscape 是 url.QueryEscape 的别名，测试里少写前缀。
func queryEscape(value string) string { return url.QueryEscape(value) }
