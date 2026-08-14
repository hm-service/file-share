package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFullFlow(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	h := a.routes()

	// 首页
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "上传队列") {
		t.Fatalf("index 页面异常: status=%d", rec.Code)
	}

	// 上传
	content := []byte("hello go fileshare")
	hash := sha256.Sum256(content)
	clientSHA := hex.EncodeToString(hash[:])

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("client_sha256", clientSHA); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req = httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	var upResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &upResp); err != nil || upResp.ID == "" {
		t.Fatalf("upload 响应异常: %s", rec.Body.String())
	}

	// 收藏
	req = httptest.NewRequest(http.MethodPost, "/api/favorite/"+upResp.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("favorite status=%d", rec.Code)
	}

	// 列表应包含收藏标记
	req = httptest.NewRequest(http.MethodGet, "/api/files", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var listResp struct {
		Files []struct {
			ID         string `json:"id"`
			IsFavorite bool   `json:"is_favorite"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil || len(listResp.Files) != 1 || !listResp.Files[0].IsFavorite {
		t.Fatalf("files 响应异常: %s", rec.Body.String())
	}

	// 下载
	req = httptest.NewRequest(http.MethodGet, "/api/download/"+upResp.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got, _ := io.ReadAll(rec.Result().Body)
	if rec.Code != http.StatusOK || !bytes.Equal(got, content) {
		t.Fatalf("download 响应异常: status=%d body=%q", rec.Code, got)
	}

	// 删除后列表为空
	req = httptest.NewRequest(http.MethodDelete, "/api/files/"+upResp.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/files", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var after struct {
		Files []any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil || len(after.Files) != 0 {
		t.Fatalf("删除后列表应为空: %s", rec.Body.String())
	}
}
