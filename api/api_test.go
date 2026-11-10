package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"file-share/api"
	"file-share/app"
	"file-share/core"
	"file-share/db"
	"file-share/internal/testenv"
)

func TestMain(m *testing.M) {
	// 集成测试会打大量访问日志：默认丢掉，需要断言日志的用例自己接管。
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func newAPI(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	instance := testenv.New(t)
	return instance, api.NewHandler(instance)
}

func detailOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Detail string `json:"detail"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	return payload.Detail
}

// groupRow 是 /api/files 里一条分组的形状。
type groupRow struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

func assertJSONError(t *testing.T, recorder *httptest.ResponseRecorder, status int, detail string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("状态码 = %d，想要 %d（body=%s）", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q，想要 JSON", got)
	}
	if got := detailOf(t, recorder); got != detail {
		t.Errorf("detail = %q，想要 %q", got, detail)
	}
}

// 除登录外的 /api/* 一律要求 Bearer。
func TestAuthRequiredRoutes(t *testing.T) {
	_, handler := newAPI(t)
	routes := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/files"},
		{http.MethodPost, "/api/check_file"},
		{http.MethodPost, "/api/upload"},
		{http.MethodPost, "/api/files/abc/favorite"},
		{http.MethodPost, "/api/files/abc/public"},
		{http.MethodGet, "/api/files/abc/download"},
		{http.MethodGet, "/api/files/abc/download-ticket"},
		{http.MethodPost, "/api/groups"},
		{http.MethodPost, "/api/groups/rename"},
		{http.MethodPut, "/api/groups/order"},
		{http.MethodPatch, "/api/files/abc"},
		{http.MethodDelete, "/api/files/abc"},
		{http.MethodPut, "/api/files/order"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			recorder := testenv.Do(handler, route.method, route.target, "")
			assertJSONError(t, recorder, http.StatusUnauthorized, "需要登录")
			if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q", got)
			}
		})
	}
}

func TestAuthCredentialErrors(t *testing.T) {
	instance, handler := newAPI(t)
	expired := testenv.ExpiredToken(t, instance)

	cases := []struct {
		name   string
		header string
		detail string
	}{
		{name: "乱写的 token", header: "Bearer not-a-session", detail: "登录凭据格式无效"},
		{name: "过期 token", header: "Bearer " + expired, detail: "登录已过期，请重新登录"},
		{name: "不是 Bearer", header: "Basic abc", detail: "需要登录"},
		{name: "Bearer 后为空", header: "Bearer ", detail: "需要登录"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			recorder := testenv.DoRequest(handler, http.MethodGet, "/api/files", nil, "", "",
				map[string]string{"Authorization": item.header})
			assertJSONError(t, recorder, http.StatusUnauthorized, item.detail)
			if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q", got)
			}
		})
	}
}

func TestLoginFlow(t *testing.T) {
	_, handler := newAPI(t)

	start := time.Now()
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/auth", map[string]any{"token": testenv.AuthToken}, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	// 整个登录请求至少要 1s + 抖动。
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("登录耗时 %v，应当至少 1s", elapsed)
	}

	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	if payload.Token == "" {
		t.Fatal("登录响应缺少 token")
	}
	if want := time.Now().Add(30 * 24 * time.Hour).Unix(); payload.ExpiresAt < want-5 || payload.ExpiresAt > want+5 {
		t.Errorf("expires_at = %d，想要约 %d（30 天后）", payload.ExpiresAt, want)
	}
	// 拿到的凭据能直接访问需要登录的接口。
	recorder = testenv.Do(handler, http.MethodGet, "/api/files", payload.Token)
	if recorder.Code != http.StatusOK {
		t.Errorf("用登录凭据访问 /api/files = %d", recorder.Code)
	}
}

func TestLoginFailures(t *testing.T) {
	_, handler := newAPI(t)

	t.Run("令牌不对", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPost, "/api/auth", map[string]any{"token": "wrong_token_000000"}, "")
		assertJSONError(t, recorder, http.StatusUnauthorized, "认证令牌不正确")
		if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("WWW-Authenticate = %q", got)
		}
	})

	t.Run("错误文案走 Accept-Language", func(t *testing.T) {
		recorder := testenv.DoRequest(handler, http.MethodPost, "/api/auth",
			bytes.NewReader([]byte(`{"token":"wrong_token_000000"}`)), "application/json", "",
			map[string]string{"Accept-Language": "en-US"})
		assertJSONError(t, recorder, http.StatusUnauthorized, "Incorrect access token")
		if got := recorder.Header().Get("Content-Language"); got != "en" {
			t.Errorf("Content-Language = %q，想要 en", got)
		}
	})

	t.Run("请求体不是 JSON", func(t *testing.T) {
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/auth",
			bytes.NewReader([]byte("not json")), "application/json", "")
		assertJSONError(t, recorder, http.StatusBadRequest, "请求体不是合法的 JSON")
	})

	t.Run("请求体字段类型不对", func(t *testing.T) {
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/auth",
			bytes.NewReader([]byte(`{"token":123}`)), "application/json", "")
		assertJSONError(t, recorder, http.StatusBadRequest, "请求体不是合法的 JSON")
	})

	t.Run("请求体超过 4 KiB", func(t *testing.T) {
		body := bytes.NewReader(bytes.Repeat([]byte("a"), 5<<10))
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/auth", body, "application/json", "")
		assertJSONError(t, recorder, http.StatusRequestEntityTooLarge, "请求体过大")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/auth", "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d，想要 405", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q，想要 POST", got)
		}
	})
}

func TestListFiles(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)

	recorder := testenv.Do(handler, http.MethodGet, "/api/files", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	var empty struct {
		Files []json.RawMessage `json:"files"`
	}
	testenv.DecodeInto(t, recorder, &empty)
	if empty.Files == nil || len(empty.Files) != 0 {
		t.Fatalf("空列表应当序列化成 []，得到 %s", recorder.Body.String())
	}

	// 直接往库里插记录：插入顺序就是列表顺序（sort_order 递增）。
	records := []db.File{
		{ID: "bbbb", Filename: "b.txt", SHA256: "b1", Size: 1, CreatedAt: 100},
		{ID: "aaaa", Filename: "a.txt", SHA256: "a1", Size: 2, CreatedAt: 200, IsPublic: true},
		{ID: "cccc", Filename: "c.txt", SHA256: "c1", Size: 3, CreatedAt: 200, IsFavorite: true},
	}
	for _, record := range records {
		if err := instance.Store.InsertFile(record); err != nil {
			t.Fatalf("插入记录失败: %v", err)
		}
	}

	recorder = testenv.Do(handler, http.MethodGet, "/api/files", token)
	var payload struct {
		Groups []groupRow `json:"groups"`
		Files  []struct {
			ID         string `json:"id"`
			Filename   string `json:"filename"`
			SHA256     string `json:"sha256"`
			Size       int64  `json:"size"`
			IsFavorite bool   `json:"is_favorite"`
			IsPublic   bool   `json:"is_public"`
			CreatedAt  int64  `json:"created_at"`
			GroupPath  string `json:"group_path"`
			SortOrder  int64  `json:"sort_order"`
		} `json:"files"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	gotIDs := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		gotIDs = append(gotIDs, file.ID)
	}
	if strings.Join(gotIDs, ",") != "bbbb,aaaa,cccc" {
		t.Errorf("列表顺序 = %v，想要 [bbbb aaaa cccc]", gotIDs)
	}
	for i, file := range payload.Files {
		if file.SortOrder != int64(i+1) {
			t.Errorf("%s sort_order = %d，想要 %d", file.ID, file.SortOrder, i+1)
		}
		if file.GroupPath != "" {
			t.Errorf("%s group_path = %q，想要空串", file.ID, file.GroupPath)
		}
	}
	if last := payload.Files[len(payload.Files)-1]; last.CreatedAt != 200 || last.Filename != "c.txt" || last.Size != 3 {
		t.Errorf("字段映射不对: %+v", last)
	}
	if !payload.Files[1].IsPublic || !payload.Files[2].IsFavorite {
		t.Error("收藏 / 公开标记没有映射出来")
	}
	if len(payload.Groups) != 0 {
		t.Errorf("没有分组时 groups = %v，想要空数组", payload.Groups)
	}
}

func TestCheckFile(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)

	content := []byte("instant content")
	sha := testenv.SHA256(content)
	if err := os.WriteFile(testenv.BlobPath(instance, sha), content, 0o600); err != nil {
		t.Fatalf("准备 blob 失败: %v", err)
	}

	t.Run("blob 已存在时秒传", func(t *testing.T) {
		body, contentType := testenv.FieldBody(t, "client_sha256", sha, "filename", "instant.txt")
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/check_file", body, contentType, token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Exists   bool   `json:"exists"`
			ID       string `json:"id"`
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		}
		testenv.DecodeInto(t, recorder, &payload)
		if !payload.Exists || payload.ID == "" || payload.Filename != "instant.txt" || payload.Size != int64(len(content)) {
			t.Fatalf("秒传响应不对: %+v", payload)
		}
		// 秒传也要落到列表里。
		list := testenv.Do(handler, http.MethodGet, "/api/files", token)
		if !strings.Contains(list.Body.String(), payload.ID) {
			t.Errorf("秒传记录没出现在列表里: %s", list.Body.String())
		}
	})

	t.Run("blob 不存在", func(t *testing.T) {
		missing := testenv.SHA256([]byte("never uploaded"))
		body, contentType := testenv.FieldBody(t, "client_sha256", missing, "filename", "a.txt")
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/check_file", body, contentType, token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Exists bool `json:"exists"`
		}
		testenv.DecodeInto(t, recorder, &payload)
		if payload.Exists {
			t.Error("不存在的 blob 应当 exists=false")
		}
	})

	invalidCases := []struct {
		name   string
		fields []string
		detail string
	}{
		{name: "缺字段", fields: nil, detail: "缺少上传文件"},
		{name: "sha 格式错", fields: []string{"client_sha256", "xyz", "filename", "a.txt"}, detail: "文件指纹格式无效"},
		{name: "文件名带路径", fields: []string{"client_sha256", sha, "filename", "dir/a.txt"}, detail: "文件名无效"},
	}
	for _, item := range invalidCases {
		t.Run(item.name, func(t *testing.T) {
			body, contentType := testenv.FieldBody(t, item.fields...)
			recorder := testenv.DoRaw(handler, http.MethodPost, "/api/check_file", body, contentType, token)
			assertJSONError(t, recorder, http.StatusBadRequest, item.detail)
		})
	}

	t.Run("不是 multipart", func(t *testing.T) {
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/check_file",
			bytes.NewReader([]byte("plain")), "application/json", token)
		assertJSONError(t, recorder, http.StatusBadRequest, "缺少上传文件")
	})

	t.Run("请求体超过 1 MiB", func(t *testing.T) {
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/check_file",
			bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20)), "multipart/form-data; boundary=x", token)
		assertJSONError(t, recorder, http.StatusRequestEntityTooLarge, "请求体过大")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/check_file", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d，想要 405", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q，想要 POST", got)
		}
	})
}

func uploadRequest(t *testing.T, handler http.Handler, token, filename string, content []byte, sha string, shaFirst bool) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := testenv.MultipartBody(t, filename, content, sha, shaFirst)
	return testenv.DoRaw(handler, http.MethodPost, "/api/upload", body, contentType, token)
}

func TestUpload(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)

	t.Run("正常上传（sha 在前）", func(t *testing.T) {
		content := []byte("uploaded via api")
		sha := testenv.SHA256(content)
		recorder := uploadRequest(t, handler, token, "hello.txt", content, sha, true)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}
		testenv.DecodeInto(t, recorder, &payload)
		if payload.ID == "" || payload.SHA256 != sha {
			t.Fatalf("上传响应不对: %+v", payload)
		}
		blob, err := os.ReadFile(testenv.BlobPath(instance, sha))
		if err != nil || !bytes.Equal(blob, content) {
			t.Fatalf("blob 内容不对: %q err=%v", blob, err)
		}
		if leftovers := testenv.UploadTempFiles(t, instance); len(leftovers) != 0 {
			t.Errorf("成功路径不该留下临时文件: %v", leftovers)
		}
	})

	t.Run("字段顺序颠倒（sha 在后）", func(t *testing.T) {
		content := []byte("file part comes first")
		sha := testenv.SHA256(content)
		recorder := uploadRequest(t, handler, token, "first.bin", content, sha, false)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("同内容重复上传指向同一个 blob", func(t *testing.T) {
		content := []byte("dedup content")
		sha := testenv.SHA256(content)
		for i := 0; i < 2; i++ {
			recorder := uploadRequest(t, handler, token, "dup.txt", content, sha, true)
			if recorder.Code != http.StatusOK {
				t.Fatalf("第 %d 次上传状态码 = %d（body=%s）", i, recorder.Code, recorder.Body.String())
			}
		}
		var count int
		files, err := instance.ListFiles()
		if err != nil {
			t.Fatalf("ListFiles() 报错: %v", err)
		}
		for _, file := range files {
			if file.SHA256 == sha {
				count++
			}
		}
		if count != 2 {
			t.Errorf("同内容记录数 = %d，想要 2", count)
		}
		entries, err := os.ReadDir(instance.Config.DataDir + "/files")
		if err != nil {
			t.Fatalf("读 files 目录失败: %v", err)
		}
		blobs := 0
		for _, entry := range entries {
			if entry.Name() == sha {
				blobs++
			}
		}
		if blobs != 1 {
			t.Errorf("同内容 blob 文件数 = %d，想要 1", blobs)
		}
	})

	errorCases := []struct {
		name     string
		filename string
		content  []byte
		sha      string
		status   int
		detail   string
	}{
		{name: "校验和不匹配", filename: "a.txt", content: []byte("data"), sha: testenv.SHA256([]byte("other")), status: 400, detail: "文件校验失败，上传内容可能已损坏"},
		{name: "sha 格式错", filename: "a.txt", content: []byte("data"), sha: "zzz", status: 400, detail: "文件指纹格式无效"},
		{name: "文件名带反斜杠", filename: "dir\\a.txt", content: []byte("data"), sha: testenv.SHA256([]byte("data")), status: 400, detail: "文件名无效"},
	}
	for _, item := range errorCases {
		t.Run(item.name, func(t *testing.T) {
			recorder := uploadRequest(t, handler, token, item.filename, item.content, item.sha, true)
			assertJSONError(t, recorder, item.status, item.detail)
			if leftovers := testenv.UploadTempFiles(t, instance); len(leftovers) != 0 {
				t.Errorf("错误路径不该留下临时文件: %v", leftovers)
			}
		})
	}

	t.Run("缺少 file 字段", func(t *testing.T) {
		body, contentType := testenv.FieldBody(t, "client_sha256", testenv.SHA256([]byte("x")))
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/upload", body, contentType, token)
		assertJSONError(t, recorder, http.StatusBadRequest, "缺少上传文件")
	})

	t.Run("缺少 client_sha256 字段", func(t *testing.T) {
		// 只发 file 一个字段，不带 client_sha256。
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("file", "a.txt")
		if err != nil {
			t.Fatalf("创建 file 字段失败: %v", err)
		}
		if _, err := part.Write([]byte("x")); err != nil {
			t.Fatalf("写内容失败: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("关闭 multipart 失败: %v", err)
		}
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/upload", body, writer.FormDataContentType(), token)
		assertJSONError(t, recorder, http.StatusBadRequest, "缺少上传文件")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/upload", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d，想要 405", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q，想要 POST", got)
		}
	})
}

func TestUploadSizeLimit(t *testing.T) {
	instance := testenv.NewWith(t, func(cfg *app.Config) { cfg.MaxFileSize = 64 })
	handler := api.NewHandler(instance)
	token := testenv.Token(t, instance)

	t.Run("文件超过 MAX_FILE_SIZE", func(t *testing.T) {
		content := bytes.Repeat([]byte("x"), 100)
		recorder := uploadRequest(t, handler, token, "big.bin", content, testenv.SHA256(content), true)
		assertJSONError(t, recorder, http.StatusRequestEntityTooLarge, "文件超过上传上限")
		if leftovers := testenv.UploadTempFiles(t, instance); len(leftovers) != 0 {
			t.Errorf("超限路径不该留下临时文件: %v", leftovers)
		}
		if testenv.BlobExists(instance, testenv.SHA256(content)) {
			t.Error("超限的上传不该落成 blob")
		}
	})

	t.Run("请求体超过上限 + 1 MiB", func(t *testing.T) {
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/upload",
			bytes.NewReader(bytes.Repeat([]byte("a"), (1<<20)+128)), "multipart/form-data; boundary=x", token)
		assertJSONError(t, recorder, http.StatusRequestEntityTooLarge, "文件超过上传上限")
	})
}

func TestFavoriteAndPublicToggles(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	record := testenv.SeedUpload(t, instance, "toggle.txt", []byte("toggle me"))

	t.Run("收藏切换", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/api/files/"+record.ID+"/favorite", token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			IsFavorite bool `json:"is_favorite"`
		}
		testenv.DecodeInto(t, recorder, &payload)
		if !payload.IsFavorite {
			t.Error("第一次切换后应当是收藏")
		}
		list := testenv.Do(handler, http.MethodGet, "/api/files", token)
		if !strings.Contains(list.Body.String(), `"is_favorite":true`) {
			t.Errorf("列表里没有收藏标记: %s", list.Body.String())
		}

		recorder = testenv.Do(handler, http.MethodPost, "/api/files/"+record.ID+"/favorite", token)
		testenv.DecodeInto(t, recorder, &payload)
		if payload.IsFavorite {
			t.Error("第二次切换后应当取消收藏")
		}
	})

	t.Run("公开切换", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/api/files/"+record.ID+"/public", token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		var payload struct {
			IsPublic bool `json:"is_public"`
		}
		testenv.DecodeInto(t, recorder, &payload)
		if !payload.IsPublic {
			t.Error("切换后应当是公开")
		}
	})

	t.Run("记录不存在", func(t *testing.T) {
		assertJSONError(t, testenv.Do(handler, http.MethodPost, "/api/files/absent/favorite", token),
			http.StatusNotFound, "找不到该文件")
		assertJSONError(t, testenv.Do(handler, http.MethodPost, "/api/files/absent/public", token),
			http.StatusNotFound, "找不到该文件")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/files/"+record.ID+"/favorite", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q", got)
		}
	})
}

func TestDownloadRequiresLogin(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	content := []byte("download body")
	record := testenv.SeedUpload(t, instance, "下载.txt", content)

	recorder := testenv.Do(handler, http.MethodGet, "/api/files/"+record.ID+"/download", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	disposition := recorder.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") {
		t.Errorf("Content-Disposition = %q，应当是附件", disposition)
	}
	if !strings.Contains(disposition, "filename*=utf-8''") {
		t.Errorf("非 ASCII 文件名应当走 RFC 2231: %q", disposition)
	}
	if !bytes.Equal(recorder.Body.Bytes(), content) {
		t.Errorf("下载内容 = %q", recorder.Body.Bytes())
	}
}

func TestDownloadBehaviour(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	content := []byte("0123456789")
	record := testenv.SeedUpload(t, instance, "range.bin", content)

	t.Run("Range 请求", func(t *testing.T) {
		recorder := testenv.DoRequest(handler, http.MethodGet, "/api/files/"+record.ID+"/download",
			nil, "", token, map[string]string{"Range": "bytes=0-3"})
		if recorder.Code != http.StatusPartialContent {
			t.Fatalf("状态码 = %d，想要 206", recorder.Code)
		}
		if got := recorder.Body.String(); got != "0123" {
			t.Errorf("Range 响应体 = %q", got)
		}
		if got := recorder.Header().Get("Content-Range"); !strings.Contains(got, "bytes 0-3/10") {
			t.Errorf("Content-Range = %q", got)
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodHead, "/api/files/"+record.ID+"/download", token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("HEAD 不该有响应体: %q", recorder.Body.String())
		}
	})

	t.Run("记录不存在", func(t *testing.T) {
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/api/files/absent/download", token),
			http.StatusNotFound, "找不到该文件")
	})

	t.Run("blob 丢失", func(t *testing.T) {
		lost := testenv.SeedUpload(t, instance, "lost.bin", []byte("will be removed"))
		testenv.RemoveBlob(t, instance, lost.SHA256)
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/api/files/"+lost.ID+"/download", token),
			http.StatusNotFound, "找不到该文件")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/api/files/"+record.ID+"/download", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q，想要 GET, HEAD", got)
		}
	})
}

func TestDeleteFile(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	record := testenv.SeedUpload(t, instance, "delete.txt", []byte("delete me"))

	recorder := testenv.Do(handler, http.MethodDelete, "/api/files/"+record.ID, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d，想要 204（body=%s）", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("204 不该有响应体: %q", recorder.Body.String())
	}
	if testenv.BlobExists(instance, record.SHA256) {
		t.Error("最后一个引用删掉后 blob 应当被删")
	}
	assertJSONError(t, testenv.Do(handler, http.MethodDelete, "/api/files/"+record.ID, token),
		http.StatusNotFound, "找不到该文件")
}

func TestPublicDownload(t *testing.T) {
	instance, handler := newAPI(t)
	content := []byte("public content")
	record := testenv.SeedUpload(t, instance, "public.txt", content)

	t.Run("私有记录匿名访问回 404", func(t *testing.T) {
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/s/"+record.ID, ""), http.StatusNotFound, "找不到该文件")
	})

	t.Run("公开后匿名可下载", func(t *testing.T) {
		if _, err := instance.TogglePublic(record.ID); err != nil {
			t.Fatalf("TogglePublic() 报错: %v", err)
		}
		recorder := testenv.Do(handler, http.MethodGet, "/s/"+record.ID, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		if !bytes.Equal(recorder.Body.Bytes(), content) {
			t.Errorf("下载内容 = %q", recorder.Body.Bytes())
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("Content-Type = %q", got)
		}
		if disposition := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
			t.Errorf("Content-Disposition = %q", disposition)
		}

		head := testenv.Do(handler, http.MethodHead, "/s/"+record.ID, "")
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			t.Errorf("HEAD /s/ = %d，体长 %d", head.Code, head.Body.Len())
		}

		ranged := testenv.DoRequest(handler, http.MethodGet, "/s/"+record.ID, nil, "", "",
			map[string]string{"Range": "bytes=0-2"})
		if ranged.Code != http.StatusPartialContent {
			t.Fatalf("Range 状态码 = %d，想要 206", ranged.Code)
		}
		if got := ranged.Body.String(); !strings.HasPrefix(string(content), got) || got == "" {
			t.Errorf("Range 响应体 = %q", got)
		}
	})

	t.Run("私有与不存在回同一句话", func(t *testing.T) {
		private := testenv.SeedUpload(t, instance, "private.txt", []byte("private"))
		privateRecorder := testenv.Do(handler, http.MethodGet, "/s/"+private.ID, "")
		missingRecorder := testenv.Do(handler, http.MethodGet, "/s/does-not-exist", "")
		if privateRecorder.Code != missingRecorder.Code {
			t.Fatalf("私有 %d 与不存在 %d 状态码应当一致", privateRecorder.Code, missingRecorder.Code)
		}
		if privateRecorder.Body.String() != missingRecorder.Body.String() {
			t.Errorf("私有与不存在的响应体应当一致: %q vs %q",
				privateRecorder.Body.String(), missingRecorder.Body.String())
		}
	})

	t.Run("blob 丢失也是一样的 404", func(t *testing.T) {
		lost := testenv.SeedUpload(t, instance, "lost-public.txt", []byte("gone"))
		if _, err := instance.TogglePublic(lost.ID); err != nil {
			t.Fatalf("TogglePublic() 报错: %v", err)
		}
		testenv.RemoveBlob(t, instance, lost.SHA256)
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/s/"+lost.ID, ""), http.StatusNotFound, "找不到该文件")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/s/"+record.ID, "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q", got)
		}
	})

	t.Run("坏路径与私有一样是 404", func(t *testing.T) {
		for _, target := range []string{"/s/", "/s/a/b"} {
			recorder := testenv.Do(handler, http.MethodGet, target, "")
			if recorder.Code != http.StatusNotFound {
				t.Errorf("%s 状态码 = %d，想要 404", target, recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("%s Content-Type = %q", target, got)
			}
		}
	})
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	_, handler := newAPI(t)

	t.Run("未知 API 是 JSON 404", func(t *testing.T) {
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/api/nope", ""), http.StatusNotFound, "资源不存在")
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/api/auth/extra", ""), http.StatusNotFound, "资源不存在")
	})

	t.Run("方法不对是 JSON 405 且带 Allow", func(t *testing.T) {
		cases := []struct {
			method string
			target string
			allow  string
		}{
			{http.MethodPost, "/api/files", "GET, HEAD"},
			{http.MethodGet, "/api/upload", "POST"},
			{http.MethodPut, "/api/files/abc", "PATCH, DELETE"},
			{http.MethodGet, "/api/files/abc", "PATCH, DELETE"},
			{http.MethodGet, "/api/files/abc/favorite", "POST"},
			{http.MethodGet, "/api/files/abc/public", "POST"},
		}
		for _, item := range cases {
			recorder := testenv.Do(handler, item.method, item.target, "")
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s 状态码 = %d，想要 405", item.method, item.target, recorder.Code)
				continue
			}
			if got := recorder.Header().Get("Allow"); got != item.allow {
				t.Errorf("%s %s Allow = %q，想要 %q", item.method, item.target, got, item.allow)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("%s %s Content-Type = %q", item.method, item.target, got)
			}
		}
	})

	t.Run("批量排序路径只接受 PUT", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/api/files/order", "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST /api/files/order 状态码 = %d，想要 405", recorder.Code)
		}
	})

	t.Run("其它接口的请求体上限是 64 KiB", func(t *testing.T) {
		recorder := testenv.DoRaw(handler, http.MethodPost, "/api/files",
			bytes.NewReader(bytes.Repeat([]byte("a"), 100<<10)), "application/json", "")
		assertJSONError(t, recorder, http.StatusRequestEntityTooLarge, "请求体过大")
	})

	t.Run("非 API 路径回纯文本 404", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/nope", "")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("Content-Type = %q，页面兜底不该是 JSON", got)
		}
	})

	t.Run("探针方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/readyz", "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q", got)
		}
	})
}

func TestLanguageNegotiation(t *testing.T) {
	_, handler := newAPI(t)

	cases := []struct {
		name       string
		header     string
		wantDetail string
		wantLang   string
	}{
		{name: "默认中文", header: "", wantDetail: "需要登录", wantLang: "zh-Hans"},
		{name: "zh-CN", header: "zh-CN,zh;q=0.9", wantDetail: "需要登录", wantLang: "zh-Hans"},
		{name: "英文", header: "en-US,en;q=0.9", wantDetail: "Sign-in required", wantLang: "en"},
		{name: "认不出用默认", header: "fr-FR", wantDetail: "需要登录", wantLang: "zh-Hans"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			headers := map[string]string{"Accept-Language": item.header}
			recorder := testenv.DoRequest(handler, http.MethodGet, "/api/files", nil, "", "", headers)
			if got := detailOf(t, recorder); got != item.wantDetail {
				t.Errorf("detail = %q，想要 %q", got, item.wantDetail)
			}
			if got := recorder.Header().Get("Content-Language"); got != item.wantLang {
				t.Errorf("Content-Language = %q，想要 %q", got, item.wantLang)
			}
			if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Accept-Language") {
				t.Errorf("Vary = %q，应当包含 Accept-Language", got)
			}
		})
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	_, handler := newAPI(t)
	for _, target := range []string{"/livez", "/readyz", "/api/nope", "/api/files"} {
		t.Run(target, func(t *testing.T) {
			recorder := testenv.Do(handler, http.MethodGet, target, "")
			header := recorder.Header()
			if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			if got := header.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q", got)
			}
			if got := header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q", got)
			}
			if got := header.Get("Content-Security-Policy"); got != api.ContentSecurityPolicy {
				t.Errorf("CSP = %q，想要 %q", got, api.ContentSecurityPolicy)
			}
			// 同源部署，不加 CORS 头。
			if got := header.Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("不该有 CORS 头: %q", got)
			}
		})
	}
}

func TestProbes(t *testing.T) {
	instance, handler := newAPI(t)

	t.Run("livez 永远 200 且无体", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/livez", "")
		if recorder.Code != http.StatusOK {
			t.Errorf("状态码 = %d", recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("livez 不该有响应体: %q", recorder.Body.String())
		}
	})

	t.Run("readyz 无 token 门槛时 200", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/readyz", "")
		if recorder.Code != http.StatusOK {
			t.Errorf("状态码 = %d", recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("readyz 不该有响应体: %q", recorder.Body.String())
		}
	})

	t.Run("readyz 权限被放松时 503", func(t *testing.T) {
		if err := os.Chmod(instance.Config.DataDir, 0o755); err != nil {
			t.Fatalf("chmod 失败: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(instance.Config.DataDir, 0o700) })
		recorder := testenv.Do(handler, http.MethodGet, "/readyz", "")
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("状态码 = %d，想要 503", recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("失败原因不该写进响应体: %q", recorder.Body.String())
		}
	})
}

func TestReadyzTokenGate(t *testing.T) {
	instance := testenv.NewWith(t, func(cfg *app.Config) { cfg.ReadyzToken = testenv.ReadyzToken })
	handler := api.NewHandler(instance)

	cases := []struct {
		name   string
		target string
		status int
	}{
		{name: "没带 token", target: "/readyz", status: http.StatusUnauthorized},
		{name: "错误 token", target: "/readyz?token=wrong", status: http.StatusUnauthorized},
		{name: "正确 token", target: "/readyz?token=" + testenv.ReadyzToken, status: http.StatusOK},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			recorder := testenv.Do(handler, http.MethodGet, item.target, "")
			if recorder.Code != item.status {
				t.Errorf("状态码 = %d，想要 %d", recorder.Code, item.status)
			}
			if recorder.Body.Len() != 0 {
				t.Errorf("探针不该有响应体: %q", recorder.Body.String())
			}
		})
	}
}

// accessLogRecords 从捕获的日志里解析出访问日志记录。
func accessLogRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(buffer.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("日志不是合法 JSON: %v (%s)", err, line)
		}
		records = append(records, record)
	}
	return records
}

func TestAccessLog(t *testing.T) {
	_, handler := newAPI(t)
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				attr.Key = "timestamp"
			}
			return attr
		},
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// 静态资源与存活探针不记；接口与就绪探针要记。
	for _, target := range []string{"/livez", "/assets/app-abc123.js", "/api/nope", "/readyz"} {
		testenv.Do(handler, http.MethodGet, target, "")
	}

	records := accessLogRecords(t, buffer)
	logged := make(map[string]map[string]any)
	for _, record := range records {
		if record["msg"] != "HTTP Request" {
			continue
		}
		path, _ := record["path"].(string)
		logged[path] = record
	}
	if _, ok := logged["/livez"]; ok {
		t.Error("/livez 不该写访问日志")
	}
	if _, ok := logged["/assets/app-abc123.js"]; ok {
		t.Error("/assets/ 静态资源不该写访问日志")
	}
	for _, target := range []string{"/api/nope", "/readyz"} {
		record, ok := logged[target]
		if !ok {
			t.Errorf("%s 应当写访问日志", target)
			continue
		}
		if record["method"] != http.MethodGet {
			t.Errorf("%s method = %v", target, record["method"])
		}
		if _, ok := record["status"].(float64); !ok {
			t.Errorf("%s status 不是数字: %v", target, record["status"])
		}
		if record["client_ip"] != "192.0.2.1" {
			t.Errorf("%s client_ip = %v，想要裸 IP 192.0.2.1", target, record["client_ip"])
		}
		if duration, ok := record["duration"].(string); !ok || duration == "" {
			t.Errorf("%s duration = %v", target, record["duration"])
		}
		stamp, ok := record["timestamp"].(string)
		if !ok {
			t.Fatalf("%s 缺少 timestamp: %v", target, record)
		}
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			t.Errorf("%s timestamp 不是 RFC 3339: %q", target, stamp)
		}
		if _, ok := record["time"]; ok {
			t.Errorf("%s 不该有 time 字段", target)
		}
	}
	if len(logged) != 2 {
		t.Errorf("访问日志条数 = %d，想要 2", len(logged))
	}
}

// fileJSON 是列表与 PATCH 响应里一条记录的完整字段（契约 §5 的 file 对象 + 分组/排序）。
type fileJSON struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	IsFavorite bool   `json:"is_favorite"`
	IsPublic   bool   `json:"is_public"`
	CreatedAt  int64  `json:"created_at"`
	GroupPath  string `json:"group_path"`
	SortOrder  int64  `json:"sort_order"`
}

func listFileIDs(t *testing.T, handler http.Handler, token string) []string {
	t.Helper()
	recorder := testenv.Do(handler, http.MethodGet, "/api/files", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/files 状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Files []fileJSON `json:"files"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	ids := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		ids = append(ids, file.ID)
	}
	return ids
}

// PATCH /api/files/{id}：按需改文件名 / 分组名；改分组会把记录移到末尾，只改名保持顺序。
func TestUpdateFileRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))

	recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{"group_path": "  工作  "}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var updated fileJSON
	testenv.DecodeInto(t, recorder, &updated)
	if updated.ID != first.ID || updated.Filename != "first.txt" || updated.SHA256 != first.SHA256 || updated.Size != first.Size {
		t.Errorf("PATCH 应当返回完整记录: %+v", updated)
	}
	if updated.GroupPath != "工作" {
		t.Errorf("group_path = %q，想要 trim 后的「工作」", updated.GroupPath)
	}
	// 两条记录 sort_order 是 1、2；改分组后 first 被移到末尾，应当是 3。
	if updated.SortOrder != 3 {
		t.Errorf("sort_order = %d，想要 3（移到末尾）", updated.SortOrder)
	}
	if got := listFileIDs(t, handler, token); len(got) != 2 || got[0] != second.ID || got[1] != first.ID {
		t.Errorf("列表顺序 = %v，想要 first 在最后", got)
	}

	// 空串 = 未分组，仍然移到末尾。
	recorder = testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{"group_path": ""}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("清空分组状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	testenv.DecodeInto(t, recorder, &updated)
	if updated.GroupPath != "" {
		t.Errorf("清空分组后 group_path = %q，想要空串", updated.GroupPath)
	}

	// 只改名：文件名变了，顺序（sort_order）不动。
	beforeRename := updated.SortOrder
	recorder = testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{"filename": "改名后.txt"}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("改名状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	testenv.DecodeInto(t, recorder, &updated)
	if updated.Filename != "改名后.txt" || updated.GroupPath != "" || updated.SortOrder != beforeRename {
		t.Errorf("改名后 = %+v，想要新文件名、未分组且顺序不变", updated)
	}

	// 两个字段一起改：名字与分组都生效，记录移到末尾。
	recorder = testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+second.ID,
		map[string]any{"filename": "second-new.txt", "group_path": "归档"}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("同时改名与分组状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	testenv.DecodeInto(t, recorder, &updated)
	if updated.Filename != "second-new.txt" || updated.GroupPath != "归档" || updated.SortOrder != beforeRename+1 {
		t.Errorf("同时改两个字段后 = %+v，想要新名字、分组「归档」且移到末尾", updated)
	}

	// 空请求体：没给任何要改的字段，按契约报 400。
	assertJSONError(t, testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{}, token), http.StatusBadRequest, "请求未包含要修改的字段")

	// 非法分组名（超长 / 控制字符）。
	longName := strings.Repeat("a", core.MaxGroupNameBytes+1)
	for name, body := range map[string]map[string]any{
		"超长":   {"group_path": longName},
		"控制字符": {"group_path": "a\nb"},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID, body, token)
			assertJSONError(t, recorder, http.StatusBadRequest, "分组名无效")
		})
	}

	// 非法文件名（空串 / 路径分隔符 / 控制字符）。
	for name, value := range map[string]string{
		"空串":   "",
		"路径":   "a/b.txt",
		"控制字符": "a\nb.txt",
	} {
		t.Run("文件名"+name, func(t *testing.T) {
			recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
				map[string]any{"filename": value}, token)
			assertJSONError(t, recorder, http.StatusBadRequest, "文件名无效")
		})
	}

	// 错误文案走 Accept-Language。
	recorder = testenv.DoRequest(handler, http.MethodPatch, "/api/files/"+first.ID,
		bytes.NewReader([]byte(`{"filename":""}`)), "application/json", token,
		map[string]string{"Accept-Language": "en"})
	assertJSONError(t, recorder, http.StatusBadRequest, "Invalid file name")

	// 请求体不是合法 JSON。
	recorder = testenv.DoRaw(handler, http.MethodPatch, "/api/files/"+first.ID,
		bytes.NewReader([]byte("not json")), "application/json", token)
	assertJSONError(t, recorder, http.StatusBadRequest, "请求体不是合法的 JSON")

	// 记录不存在。
	assertJSONError(t, testenv.DoJSON(handler, http.MethodPatch, "/api/files/absent",
		map[string]any{"filename": "x.txt"}, token), http.StatusNotFound, "找不到该文件")

	// 未登录。
	assertJSONError(t, testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{"filename": "x.txt"}, ""), http.StatusUnauthorized, "需要登录")

	// 方法不对：这条路径上只允许 PATCH / DELETE。
	recorder = testenv.Do(handler, http.MethodGet, "/api/files/"+first.ID, token)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 状态码 = %d，想要 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "PATCH, DELETE" {
		t.Errorf("Allow = %q，想要 PATCH, DELETE", got)
	}
}

// PUT /api/files/order：批量重写顺序；成功 204，非法 400，记录不存在 404 且整体回滚。
func TestSetOrderRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	third := testenv.SeedUpload(t, instance, "third.txt", []byte("third"))

	order := func(items []map[string]any) *httptest.ResponseRecorder {
		return testenv.DoJSON(handler, http.MethodPut, "/api/files/order", map[string]any{"items": items}, token)
	}
	items := []map[string]any{
		{"id": third.ID, "sort_order": 10},
		{"id": first.ID, "sort_order": 20},
		{"id": second.ID, "sort_order": 30},
	}
	recorder := order(items)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("204 不该有响应体: %q", recorder.Body.String())
	}
	wantOrder := []string{third.ID, first.ID, second.ID}
	if got := listFileIDs(t, handler, token); len(got) != 3 || got[0] != wantOrder[0] || got[1] != wantOrder[1] || got[2] != wantOrder[2] {
		t.Errorf("排序后列表 = %v，想要 %v", got, wantOrder)
	}

	// 任一条记录不存在：整体 404，原顺序不变。
	badItems := append([]map[string]any{{"id": "absent", "sort_order": 1}}, items...)
	recorder = order(badItems)
	assertJSONError(t, recorder, http.StatusNotFound, "找不到该文件")
	if got := listFileIDs(t, handler, token); len(got) != 3 || got[0] != wantOrder[0] || got[1] != wantOrder[1] || got[2] != wantOrder[2] {
		t.Errorf("回滚后列表 = %v，想要原顺序 %v", got, wantOrder)
	}

	// 非法排序数据。
	invalidCases := map[string]any{
		"空列表":   []map[string]any{},
		"重复 id": []map[string]any{{"id": first.ID, "sort_order": 1}, {"id": first.ID, "sort_order": 2}},
		"空 id":  []map[string]any{{"id": "", "sort_order": 1}},
		"负数":    []map[string]any{{"id": first.ID, "sort_order": -1}},
		"超过上界":  []map[string]any{{"id": first.ID, "sort_order": core.MaxSortOrder + 1}},
	}
	for name, bad := range invalidCases {
		t.Run(name, func(t *testing.T) {
			assertJSONError(t, order(bad.([]map[string]any)), http.StatusBadRequest, "排序数据无效")
		})
	}

	// 超过 2000 项：能过请求体上限（该接口单独放宽到 256 KiB），由业务校验回 400。
	tooMany := make([]map[string]any, 0, core.MaxOrderItems+1)
	for i := 0; i <= core.MaxOrderItems; i++ {
		tooMany = append(tooMany, map[string]any{"id": fmt.Sprintf("id-%04d", i), "sort_order": i})
	}
	assertJSONError(t, order(tooMany), http.StatusBadRequest, "排序数据无效")

	// 错误文案走 Accept-Language。
	recorder = testenv.DoRequest(handler, http.MethodPut, "/api/files/order",
		bytes.NewReader([]byte(`{"items":[]}`)), "application/json", token,
		map[string]string{"Accept-Language": "en"})
	assertJSONError(t, recorder, http.StatusBadRequest, "Invalid ordering data")

	// 请求体不是合法 JSON。
	recorder = testenv.DoRaw(handler, http.MethodPut, "/api/files/order",
		bytes.NewReader([]byte("not json")), "application/json", token)
	assertJSONError(t, recorder, http.StatusBadRequest, "请求体不是合法的 JSON")

	// 未登录。
	assertJSONError(t, testenv.DoJSON(handler, http.MethodPut, "/api/files/order",
		map[string]any{"items": items}, ""), http.StatusUnauthorized, "需要登录")
}

// 并发上传同一份内容：两个请求都成功，只有一个 blob，没有残留临时文件。
func TestConcurrentDuplicateUploads(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	content := []byte("concurrent content")
	sha := testenv.SHA256(content)

	var group sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		group.Go(func() {
			recorder := uploadRequest(t, handler, token, "same.txt", content, sha, true)
			codes[i] = recorder.Code
		})
	}
	group.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("第 %d 个并发上传状态码 = %d", i, code)
		}
	}
	files, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	count := 0
	for _, file := range files {
		if file.SHA256 == sha {
			count++
		}
	}
	if count != 2 {
		t.Errorf("同内容记录数 = %d，想要 2", count)
	}
	if leftovers := testenv.UploadTempFiles(t, instance); len(leftovers) != 0 {
		t.Errorf("不该留下临时文件: %v", leftovers)
	}
	if !testenv.BlobExists(instance, sha) {
		t.Error("blob 应当存在")
	}
}

// 下载票据：登录后换成 /d?t= 短期地址，浏览器直接导航即可匿名下载（支持 Range / HEAD）。
func TestFileDownloadTicket(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	content := []byte("ticket download body")
	record := testenv.SeedUpload(t, instance, "票据.bin", content)

	recorder := testenv.Do(handler, http.MethodGet, "/api/files/"+record.ID+"/download-ticket", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		URL string `json:"url"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	if !strings.HasPrefix(payload.URL, "/d?t=") {
		t.Fatalf("url = %q，想要 /d?t=<ticket>", payload.URL)
	}

	t.Run("票据可匿名下载", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, payload.URL, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("Content-Type = %q", got)
		}
		if disposition := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
			t.Errorf("Content-Disposition = %q", disposition)
		}
		if !bytes.Equal(recorder.Body.Bytes(), content) {
			t.Errorf("下载内容 = %q", recorder.Body.Bytes())
		}
	})

	t.Run("票据同样支持 Range 与 HEAD", func(t *testing.T) {
		ranged := testenv.DoRequest(handler, http.MethodGet, payload.URL, nil, "", "",
			map[string]string{"Range": "bytes=0-3"})
		if ranged.Code != http.StatusPartialContent {
			t.Fatalf("Range 状态码 = %d，想要 206", ranged.Code)
		}
		head := testenv.Do(handler, http.MethodHead, payload.URL, "")
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			t.Errorf("HEAD /d = %d，体长 %d", head.Code, head.Body.Len())
		}
	})

	t.Run("票据无效", func(t *testing.T) {
		for _, target := range []string{"/d", "/d?t=", "/d?t=nope", "/d?t=abc.def", "/d/extra"} {
			recorder := testenv.Do(handler, http.MethodGet, target, "")
			if recorder.Code != http.StatusNotFound {
				t.Errorf("%s 状态码 = %d，想要 404", target, recorder.Code)
				continue
			}
			if recc := recorder.Header().Get("Content-Type"); recc != "application/json; charset=utf-8" {
				t.Errorf("%s Content-Type = %q", target, recc)
			}
		}
	})

	t.Run("过期票据", func(t *testing.T) {
		signer := core.NewTicketSigner([]byte(instance.Config.SessionSigningKey))
		expired, err := signer.Issue(record.ID, time.Now().Add(-2*core.DownloadTicketTTL))
		if err != nil {
			t.Fatalf("Issue() 报错: %v", err)
		}
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/d?t="+expired, ""),
			http.StatusNotFound, "下载链接无效或已过期")
	})

	t.Run("记录不存在", func(t *testing.T) {
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/api/files/absent/download-ticket", token),
			http.StatusNotFound, "找不到该文件")
	})

	t.Run("blob 丢失", func(t *testing.T) {
		lost := testenv.SeedUpload(t, instance, "lost.bin", []byte("gone"))
		testenv.RemoveBlob(t, instance, lost.SHA256)
		assertJSONError(t, testenv.Do(handler, http.MethodGet, "/api/files/"+lost.ID+"/download-ticket", token),
			http.StatusNotFound, "找不到该文件")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodPost, "/api/files/"+record.ID+"/download-ticket", token)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("票据接口 POST = %d Allow=%q", recorder.Code, recorder.Header().Get("Allow"))
		}
		recorder = testenv.Do(handler, http.MethodPost, "/d?t=x", "")
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("POST /d = %d Allow=%q", recorder.Code, recorder.Header().Get("Allow"))
		}
	})
}

// POST /api/groups/rename：批量改写分组名，成功 204；冲突 409、源分组不存在 404。
func TestRenameGroupRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	kept := testenv.SeedUpload(t, instance, "kept.txt", []byte("kept"))
	for _, body := range []struct {
		id    string
		group string
	}{
		{first.ID, "旧组"},
		{second.ID, "旧组"},
		{kept.ID, "保留组"},
	} {
		recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+body.id,
			map[string]any{"group_path": body.group}, token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("设置分组失败: %d（body=%s）", recorder.Code, recorder.Body.String())
		}
	}

	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename",
		map[string]any{"path": " 旧组 ", "new_name": " 新组 "}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("204 不该有响应体: %q", recorder.Body.String())
	}
	list := testenv.Decode(t, testenv.Do(handler, http.MethodGet, "/api/files", token))
	groups := map[string]string{}
	for _, item := range list["files"].([]any) {
		file := item.(map[string]any)
		groups[file["id"].(string)] = file["group_path"].(string)
	}
	if groups[first.ID] != "新组" || groups[second.ID] != "新组" || groups[kept.ID] != "保留组" {
		t.Fatalf("重命名后分组 = %+v", groups)
	}

	t.Run("目标分组已存在", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename",
			map[string]any{"path": "新组", "new_name": "保留组"}, token)
		assertJSONError(t, recorder, http.StatusConflict, "分组名已存在")
	})

	t.Run("源分组不存在", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename",
			map[string]any{"path": "不存在", "new_name": "任意"}, token)
		assertJSONError(t, recorder, http.StatusNotFound, "分组不存在")
	})

	t.Run("非法分组名", func(t *testing.T) {
		for name, payload := range map[string]map[string]any{
			"旧名为空": {"path": "", "new_name": "新组"},
			"新名为空": {"path": "新组", "new_name": ""},
			"超长":   {"path": "新组", "new_name": strings.Repeat("a", core.MaxGroupNameBytes+1)},
		} {
			t.Run(name, func(t *testing.T) {
				recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename", payload, token)
				assertJSONError(t, recorder, http.StatusBadRequest, "分组名无效")
			})
		}
	})

	t.Run("错误文案走 Accept-Language", func(t *testing.T) {
		// DoJSON 不带自定义头，这里用原始请求补 Accept-Language。
		recorder := testenv.DoRequest(handler, http.MethodPost, "/api/groups/rename",
			bytes.NewReader([]byte(`{"path":"新组","new_name":"保留组"}`)), "application/json", token,
			map[string]string{"Accept-Language": "en"})
		assertJSONError(t, recorder, http.StatusConflict, "A group with this name already exists")
	})

	t.Run("未登录", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename",
			map[string]any{"path": "新组", "new_name": "另一个"}, "")
		assertJSONError(t, recorder, http.StatusUnauthorized, "需要登录")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/groups/rename", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d，想要 405", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q，想要 POST", got)
		}
	})
}

// PUT /api/groups/order：按给定顺序重写分组顺序；集合对不上 409、输入非法 400。
func TestGroupOrderRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	for _, item := range []struct {
		id    string
		group string
	}{
		{first.ID, "一组"},
		{second.ID, "二组"},
	} {
		recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+item.id,
			map[string]any{"group_path": item.group}, token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("设置分组失败: %d（body=%s）", recorder.Code, recorder.Body.String())
		}
	}

	listGroups := func() []string {
		t.Helper()
		var payload struct {
			Groups []groupRow `json:"groups"`
		}
		testenv.DecodeInto(t, testenv.Do(handler, http.MethodGet, "/api/files", token), &payload)
		paths := make([]string, 0, len(payload.Groups))
		for _, group := range payload.Groups {
			paths = append(paths, group.Path)
		}
		return paths
	}
	if groups := listGroups(); len(groups) != 2 || groups[0] != "一组" || groups[1] != "二组" {
		t.Fatalf("初始 groups = %v，想要 [一组 二组]", groups)
	}

	recorder := testenv.DoJSON(handler, http.MethodPut, "/api/groups/order",
		map[string]any{"parent": "", "names": []string{"二组", "一组"}}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if groups := listGroups(); len(groups) != 2 || groups[0] != "二组" || groups[1] != "一组" {
		t.Fatalf("重排后 groups = %v，想要 [二组 一组]", groups)
	}

	t.Run("集合对不上", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPut, "/api/groups/order",
			map[string]any{"parent": "", "names": []string{"二组"}}, token)
		assertJSONError(t, recorder, http.StatusConflict, "分组已变化，请刷新后重试")
		// 失败时原顺序不变。
		if groups := listGroups(); groups[0] != "二组" {
			t.Errorf("失败后 groups = %v，顺序不该变", groups)
		}
	})

	t.Run("输入非法", func(t *testing.T) {
		for name, names := range map[string][]string{
			"重复":  {"二组", "二组"},
			"空名":  {"二组", ""},
			"带空白": {" 二组", "一组"},
		} {
			t.Run(name, func(t *testing.T) {
				recorder := testenv.DoJSON(handler, http.MethodPut, "/api/groups/order",
					map[string]any{"parent": "", "names": names}, token)
				assertJSONError(t, recorder, http.StatusBadRequest, "排序数据无效")
			})
		}
	})

	t.Run("未登录", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPut, "/api/groups/order",
			map[string]any{"parent": "", "names": []string{"二组", "一组"}}, "")
		assertJSONError(t, recorder, http.StatusUnauthorized, "需要登录")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/groups/order", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d，想要 405", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "PUT" {
			t.Errorf("Allow = %q，想要 PUT", got)
		}
	})
}

// 两级分组：子分组挂在父分组下，同级独立排序；父分组改名时子分组与文件路径一起改。
func TestNestedGroupRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))

	if recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{"group_path": "工作/项目A"}, token); recorder.Code != http.StatusOK {
		t.Fatalf("设置二级分组失败: %d（body=%s）", recorder.Code, recorder.Body.String())
	}

	listGroups := func() []groupRow {
		t.Helper()
		var payload struct {
			Groups []groupRow `json:"groups"`
		}
		testenv.DecodeInto(t, testenv.Do(handler, http.MethodGet, "/api/files", token), &payload)
		return payload.Groups
	}
	groups := listGroups()
	if len(groups) != 2 || groups[0].Path != "工作" || groups[1].Path != "工作/项目A" ||
		groups[1].Parent != "工作" || groups[1].Name != "项目A" {
		t.Fatalf("groups = %+v，想要顶层「工作」+ 子分组「工作/项目A」", groups)
	}

	// 三级路径拒绝。
	if recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups",
		map[string]any{"path": "a/b/c"}, token); recorder.Code != http.StatusBadRequest {
		t.Errorf("三级路径状态码 = %d，想要 400", recorder.Code)
	}

	// 新建子分组：追加到父级末尾。
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups",
		map[string]any{"path": "工作/项目B"}, token)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("新建子分组状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var created struct {
		Path string `json:"path"`
	}
	testenv.DecodeInto(t, recorder, &created)
	if created.Path != "工作/项目B" {
		t.Errorf("path = %q，想要 工作/项目B", created.Path)
	}

	// 同级重排：只动这个父级下的顺序。
	recorder = testenv.DoJSON(handler, http.MethodPut, "/api/groups/order",
		map[string]any{"parent": "工作", "names": []string{"项目B", "项目A"}}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("子分组重排状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	groups = listGroups()
	if len(groups) != 3 || groups[1].Path != "工作/项目B" || groups[2].Path != "工作/项目A" {
		t.Fatalf("重排后 groups = %+v", groups)
	}

	// 父分组改名：子分组路径与文件的分组路径一起改。
	recorder = testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename",
		map[string]any{"path": "工作", "new_name": "工作区"}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("父分组改名状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	groups = listGroups()
	if len(groups) != 3 || groups[0].Path != "工作区" ||
		groups[1].Path != "工作区/项目B" || groups[2].Path != "工作区/项目A" {
		t.Fatalf("改名后 groups = %+v", groups)
	}
	var files struct {
		Files []struct {
			ID        string `json:"id"`
			GroupPath string `json:"group_path"`
		} `json:"files"`
	}
	testenv.DecodeInto(t, testenv.Do(handler, http.MethodGet, "/api/files", token), &files)
	if len(files.Files) != 1 || files.Files[0].GroupPath != "工作区/项目A" {
		t.Fatalf("改名后文件 = %+v，想要 group_path 工作区/项目A", files.Files)
	}
}

// POST /api/groups：手动新建空分组，成功 201；同名 409、非法名 400。
func TestCreateGroupRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)

	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups",
		map[string]any{"path": "  新组  "}, token)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Path string `json:"path"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	if payload.Path != "新组" {
		t.Errorf("path = %q，想要 trim 后的「新组」", payload.Path)
	}
	var list struct {
		Groups []groupRow `json:"groups"`
	}
	testenv.DecodeInto(t, testenv.Do(handler, http.MethodGet, "/api/files", token), &list)
	if len(list.Groups) != 1 || list.Groups[0].Path != "新组" || list.Groups[0].Parent != "" || list.Groups[0].Name != "新组" {
		t.Errorf("空分组也应当出现在 groups 里，得到 %v", list.Groups)
	}

	// 空分组可以改名。
	recorder = testenv.DoJSON(handler, http.MethodPost, "/api/groups/rename",
		map[string]any{"path": "新组", "new_name": "改名空组"}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("改名空分组状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}

	t.Run("同名冲突", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups",
			map[string]any{"path": "改名空组"}, token)
		assertJSONError(t, recorder, http.StatusConflict, "分组名已存在")
	})

	t.Run("非法名", func(t *testing.T) {
		for name, raw := range map[string]string{
			"空名": "",
			"超长": strings.Repeat("a", core.MaxGroupNameBytes+1),
		} {
			t.Run(name, func(t *testing.T) {
				recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups",
					map[string]any{"path": raw}, token)
				assertJSONError(t, recorder, http.StatusBadRequest, "分组名无效")
			})
		}
	})

	t.Run("未登录", func(t *testing.T) {
		recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups",
			map[string]any{"path": "随便"}, "")
		assertJSONError(t, recorder, http.StatusUnauthorized, "需要登录")
	})

	t.Run("方法不对", func(t *testing.T) {
		recorder := testenv.Do(handler, http.MethodGet, "/api/groups", token)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("状态码 = %d，想要 405", recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q，想要 POST", got)
		}
	})
}

// POST /api/groups/delete：删分组，子分组级联；delete_files 决定组内文件是否一起删。
func TestDeleteGroupRoute(t *testing.T) {
	instance, handler := newAPI(t)
	token := testenv.Token(t, instance)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	for _, item := range []struct {
		id    string
		group string
	}{
		{first.ID, "工作/项目A"},
		{second.ID, "工作/项目B"},
	} {
		recorder := testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+item.id,
			map[string]any{"group_path": item.group}, token)
		if recorder.Code != http.StatusOK {
			t.Fatalf("设置分组失败: %d（body=%s）", recorder.Code, recorder.Body.String())
		}
	}

	// 保留文件：子分组级联删除，文件回到未分组。
	recorder := testenv.DoJSON(handler, http.MethodPost, "/api/groups/delete",
		map[string]any{"path": "工作"}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var list struct {
		Groups []groupRow `json:"groups"`
		Files  []struct {
			ID        string `json:"id"`
			GroupPath string `json:"group_path"`
		} `json:"files"`
	}
	testenv.DecodeInto(t, testenv.Do(handler, http.MethodGet, "/api/files", token), &list)
	if len(list.Groups) != 0 {
		t.Errorf("删除后 groups = %v，想要空", list.Groups)
	}
	for _, file := range list.Files {
		if file.GroupPath != "" {
			t.Errorf("%s 的 group_path = %q，想要空串", file.ID, file.GroupPath)
		}
	}

	// 再删同一个分组：404。
	recorder = testenv.DoJSON(handler, http.MethodPost, "/api/groups/delete",
		map[string]any{"path": "工作"}, token)
	assertJSONError(t, recorder, http.StatusNotFound, "分组不存在")

	// 删除分组并连带删文件。
	recorder = testenv.DoJSON(handler, http.MethodPost, "/api/groups", map[string]any{"path": "临时"}, token)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("建分组失败: %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	recorder = testenv.DoJSON(handler, http.MethodPatch, "/api/files/"+first.ID,
		map[string]any{"group_path": "临时"}, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("移动文件失败: %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	recorder = testenv.DoJSON(handler, http.MethodPost, "/api/groups/delete",
		map[string]any{"path": "临时", "delete_files": true}, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	testenv.DecodeInto(t, testenv.Do(handler, http.MethodGet, "/api/files", token), &list)
	if len(list.Files) != 1 || list.Files[0].ID != second.ID {
		t.Errorf("删除文件后 files = %+v，想要只剩 second", list.Files)
	}
}
