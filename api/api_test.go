package api_test

import (
	"bytes"
	"encoding/json"
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
		{http.MethodDelete, "/api/files/abc"},
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

	// 直接往库里插记录以控制创建时间与排序。
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
		Files []struct {
			ID         string `json:"id"`
			Filename   string `json:"filename"`
			SHA256     string `json:"sha256"`
			Size       int64  `json:"size"`
			IsFavorite bool   `json:"is_favorite"`
			IsPublic   bool   `json:"is_public"`
			CreatedAt  int64  `json:"created_at"`
		} `json:"files"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	gotIDs := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		gotIDs = append(gotIDs, file.ID)
	}
	// created_at 倒序；同一秒按 id 升序。
	if strings.Join(gotIDs, ",") != "aaaa,cccc,bbbb" {
		t.Errorf("列表顺序 = %v，想要 [aaaa cccc bbbb]", gotIDs)
	}
	if last := payload.Files[len(payload.Files)-1]; last.CreatedAt != 100 || last.Filename != "b.txt" || last.Size != 1 {
		t.Errorf("字段映射不对: %+v", last)
	}
	if !payload.Files[0].IsPublic || !payload.Files[1].IsFavorite {
		t.Error("收藏 / 公开标记没有映射出来")
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
	instance := testenv.NewWith(t, func(cfg *app.Config) { cfg.MaxUploadBytes = 64 })
	handler := api.NewHandler(instance)
	token := testenv.Token(t, instance)

	t.Run("文件超过 MAX_UPLOAD_BYTES", func(t *testing.T) {
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
			{http.MethodPut, "/api/files/abc", "DELETE"},
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
