// Package testenv 给测试用：一套独立的应用实例（全新临时 DATA_DIR）与几个发请求的小工具。
// 测试用固定凭据替代环境变量注入，登录接口的最短耗时只有真正验登录时才付。
package testenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"file-share/app"
	"file-share/core"
	"file-share/db"
	"file-share/httpx"
)

// 测试用的固定凭据，长度与字符集都符合生产校验。
const (
	AuthToken         = "test_auth_token_0123456789"
	SessionSigningKey = "test_session_signing_key_0123456789"
	ReadyzToken       = "test_readyz_token_0123456789"
)

// DefaultMaxUploadBytes 是测试实例的上传上限：比生产小得多，方便造 413。
const DefaultMaxUploadBytes = 1 << 20

var (
	tokenHashOnce sync.Once
	tokenHash     string
	tokenHashErr  error
)

// authTokenHash 只算一次 Argon2id：每个实例都重算的话一个包要白等好几秒。
func authTokenHash(t *testing.T) string {
	t.Helper()
	tokenHashOnce.Do(func() { tokenHash, tokenHashErr = core.HashToken(AuthToken) })
	if tokenHashErr != nil {
		t.Fatalf("计算测试令牌哈希失败: %v", tokenHashErr)
	}
	return tokenHash
}

// New 起一套独立的应用实例，用完自动清理。
func New(t *testing.T) *app.App {
	t.Helper()
	return NewWith(t, nil)
}

// NewWith 同上，但允许先改一下 Config（例如关掉 READYZ_TOKEN 门槛、调小上传上限）。
func NewWith(t *testing.T, mutate func(*app.Config)) *app.App {
	t.Helper()
	cfg := app.Config{
		DataDir:           t.TempDir(),
		TrustedProxies:    httpx.DefaultTrustedProxies,
		SessionSigningKey: SessionSigningKey,
		AuthTokenHash:     authTokenHash(t),
		ReadyzToken:       "",
		MaxUploadBytes:    DefaultMaxUploadBytes,
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

func signer(instance *app.App) *core.SessionSigner {
	return core.NewSessionSigner([]byte(instance.Config.SessionSigningKey))
}

// Token 直接签一张有效会话，不走登录接口（登录有 1 秒最短耗时，集成测试不必为它反复等待）。
func Token(t *testing.T, instance *app.App) string {
	t.Helper()
	token, _, err := signer(instance).Issue(time.Now())
	if err != nil {
		t.Fatalf("签发测试会话失败: %v", err)
	}
	return token
}

// ExpiredToken 签一张已经过期的会话，用来验证 auth.expired 与「验签先于过期」。
func ExpiredToken(t *testing.T, instance *app.App) string {
	t.Helper()
	token, _, err := signer(instance).Issue(time.Now().Add(-core.SessionTTL - time.Hour))
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

// DoRaw 发一个自定义请求体的请求。
func DoRaw(handler http.Handler, method, target string, body io.Reader, contentType, token string) *httptest.ResponseRecorder {
	return DoRequest(handler, method, target, body, contentType, token, nil)
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
	recorder := DoJSON(handler, http.MethodPost, "/api/auth", map[string]any{"token": AuthToken}, "")
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

// MultipartBody 造一份上传表单。shaFirst 控制 client_sha256 在 file 之前还是之后，
// 用来验证「字段顺序不固定」。
func MultipartBody(t *testing.T, filename string, content []byte, clientSHA string, shaFirst bool) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if shaFirst {
		if err := writer.WriteField("client_sha256", clientSHA); err != nil {
			t.Fatalf("写 client_sha256 字段失败: %v", err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建 file 字段失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写上传内容失败: %v", err)
	}
	if !shaFirst {
		if err := writer.WriteField("client_sha256", clientSHA); err != nil {
			t.Fatalf("写 client_sha256 字段失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	return body, writer.FormDataContentType()
}

// FieldBody 造一份只含字段的 multipart 表单，字段顺序按参数给定。
func FieldBody(t *testing.T, fields ...string) (*bytes.Buffer, string) {
	t.Helper()
	if len(fields)%2 != 0 {
		t.Fatalf("字段必须成对出现")
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for i := 0; i < len(fields); i += 2 {
		if err := writer.WriteField(fields[i], fields[i+1]); err != nil {
			t.Fatalf("写字段 %s 失败: %v", fields[i], err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	return body, writer.FormDataContentType()
}

// SHA256 返回内容的 sha256（小写十六进制）。
func SHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// SeedUpload 直接调应用的上传编排播一条记录，不走 HTTP。
func SeedUpload(t *testing.T, instance *app.App, filename string, content []byte) *db.File {
	t.Helper()
	upload, err := instance.NewUpload()
	if err != nil {
		t.Fatalf("创建上传失败: %v", err)
	}
	if _, err := upload.Write(content); err != nil {
		upload.Abort()
		t.Fatalf("写上传内容失败: %v", err)
	}
	record, err := instance.FinishUpload(upload, filename, SHA256(content))
	if err != nil {
		t.Fatalf("完成上传失败: %v", err)
	}
	return record
}

// BlobPath 按契约里的数据目录布局算出一个 blob 的路径。
func BlobPath(instance *app.App, sha string) string {
	return filepath.Join(instance.Config.DataDir, "files", sha)
}

// BlobExists 报告 blob 文件是否还在。
func BlobExists(instance *app.App, sha string) bool {
	_, err := os.Stat(BlobPath(instance, sha))
	return err == nil
}

// UploadTempFiles 列出 files/ 下残留的上传临时文件（任何错误路径都不该留下它们）。
func UploadTempFiles(t *testing.T, instance *app.App) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(instance.Config.DataDir, "files"))
	if err != nil {
		t.Fatalf("读取 files 目录失败: %v", err)
	}
	var leftovers []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".upload-") && strings.HasSuffix(name, ".tmp") {
			leftovers = append(leftovers, name)
		}
	}
	return leftovers
}

// RemoveBlob 删掉 blob 文件，用来模拟「记录在而物理文件丢失」。
func RemoveBlob(t *testing.T, instance *app.App, sha string) {
	t.Helper()
	if err := os.Remove(BlobPath(instance, sha)); err != nil {
		t.Fatalf("删除 blob 失败: %v", err)
	}
}
