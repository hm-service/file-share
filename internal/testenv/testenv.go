// Package testenv 给测试用：一套独立的应用实例（全新临时 DATA_DIR 与只读 FILES_DIR）与几个发请求的小工具。
// 测试用固定凭据替代环境变量注入，登录接口的最短耗时只有真正验登录时才付。
package testenv

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"file-share/app"
	"file-share/core"
	"file-share/httpx"
)

// 测试用的固定凭据，长度与字符集都符合生产校验。
const (
	// UserName / Password 是默认测试用户的登录凭据（只读 + 可收藏，可见整个数据目录）。
	UserName          = "tester"
	Password          = "tester_password_0123456789"
	SessionSigningKey = "test_session_signing_key_0123456789"
	URLSignatureKey   = "test_url_signature_key_0123456789"
	ReadyzToken       = "test_readyz_token_0123456789"
)

// DefaultUsers 是一套只读用户表：默认用户能浏览整个数据目录，也能收藏。
func DefaultUsers() []app.User {
	return []app.User{{
		Name:        UserName,
		Password:    Password,
		Permissions: app.PermissionRead | app.PermissionFav,
		Access:      []string{"/"},
	}}
}

// DefaultFiles 是一份固定的只读测试数据（相对路径 -> 内容）。
// 额外会建出空目录 “空目录/子目录”，用来验证空分组也出现在列表里。
func DefaultFiles() map[string]string {
	return map[string]string{
		"软件/安卓应用/app.apk":          "apk-content",
		"软件/说明.md":                 "# 说明\n",
		"软件/运行库/VC++/vcredist.exe": "vcredist",
		"我的世界/地图/map.zip":          "map-content",
		"根目录文件.txt":                "root-file",
	}
}

// New 起一套独立的应用实例，用完自动清理。
func New(t *testing.T) *app.App {
	t.Helper()
	return NewWith(t, nil, nil)
}

// NewWith 允许覆盖测试数据（nil 用 DefaultFiles）并调整配置。
func NewWith(t *testing.T, files map[string]string, mutate func(*app.Config)) *app.App {
	t.Helper()
	dataDir := t.TempDir()
	filesDir := t.TempDir()
	if files == nil {
		files = DefaultFiles()
	}
	if err := os.MkdirAll(filepath.Join(filesDir, "空目录", "子目录"), 0o755); err != nil {
		t.Fatalf("创建空目录失败: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(filesDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("写测试文件失败: %v", err)
		}
	}
	cfg := app.Config{
		DataDir:           dataDir,
		FilesDir:          filesDir,
		TrustedProxies:    httpx.DefaultTrustedProxies,
		SessionSigningKey: SessionSigningKey,
		URLSignatureKey:   URLSignatureKey,
		ReadyzToken:       "",
		Users:             DefaultUsers(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	instance, err := app.Open(cfg)
	if err != nil {
		t.Fatalf("打开测试实例失败: %v", err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

// FilesDir 返回测试实例的只读数据目录：需要临时改数据（例如模拟 SMB 侧增删）时用。
func FilesDir(instance *app.App) string { return instance.Config.FilesDir }

// WriteFile 在只读数据目录里新建 / 覆盖一个文件（模拟 SMB 侧的变化）。
func WriteFile(t *testing.T, instance *app.App, rel, content string) {
	t.Helper()
	full := filepath.Join(FilesDir(instance), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func signer(instance *app.App) *core.SessionSigner {
	return core.NewSessionSigner([]byte(instance.Config.SessionSigningKey))
}

// Token 直接签一张有效会话，不走登录接口（登录有 1 秒最短耗时，集成测试不必为它反复等待）。
func Token(t *testing.T, instance *app.App) string {
	t.Helper()
	return TokenFor(t, instance, UserName)
}

// TokenFor 给指定用户签一张有效会话，用来测权限差异。
func TokenFor(t *testing.T, instance *app.App, username string) string {
	t.Helper()
	token, _, err := signer(instance).Issue(time.Now(), username)
	if err != nil {
		t.Fatalf("签发测试会话失败: %v", err)
	}
	return token
}

// ExpiredToken 签一张已经过期的会话，用来验证 auth.expired 与「验签先于过期」。
func ExpiredToken(t *testing.T, instance *app.App) string {
	t.Helper()
	token, _, err := signer(instance).Issue(time.Now().Add(-core.SessionTTL-time.Hour), UserName)
	if err != nil {
		t.Fatalf("签发生过期会话失败: %v", err)
	}
	return token
}

// Do 发一个不带请求体的请求；token 非空时带 Bearer 头。
func Do(handler http.Handler, method, target, token string) *httptest.ResponseRecorder {
	return DoRequest(handler, method, target, nil, "", token, nil)
}

// DoJSON 发一个 JSON 请求。
func DoJSON(handler http.Handler, method, target string, payload any, token string) *httptest.ResponseRecorder {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		body = bytes.NewReader(encoded)
	}
	return DoRequest(handler, method, target, body, "application/json", token, nil)
}

// DoRequest 发一个请求并返回记录器。
func DoRequest(handler http.Handler, method, target string, body io.Reader, contentType, token string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// Login 走一遍真实登录接口，返回会话凭据与过期时间。
func Login(t *testing.T, handler http.Handler) (token string, expiresAt int64) {
	t.Helper()
	recorder := DoJSON(handler, http.MethodPost, "/api/auth", map[string]any{"username": UserName, "password": Password}, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录失败: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	DecodeInto(t, recorder, &payload)
	if payload.Token == "" {
		t.Fatalf("登录响应缺少 token: %s", recorder.Body.String())
	}
	return payload.Token, payload.ExpiresAt
}

func Decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := map[string]any{}
	DecodeInto(t, recorder, &payload)
	return payload
}

func DecodeInto(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("响应不是合法的 JSON: %v (%s)", err, recorder.Body.String())
	}
}
