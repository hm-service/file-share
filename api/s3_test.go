package api_test

import (
	"file-share/app"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"file-share/api"
	"file-share/internal/testenv"
)

// TestS3UploadDownload 覆盖 S3 后端下的完整链路：上传落对象、秒传、下载
// （Range / HEAD / Content-Disposition）、引用计数删除。
// 需要 S3_TEST_* 环境变量；未设置时跳过。
func TestS3UploadDownload(t *testing.T) {
	instance, cfg := testenv.NewS3(t)
	handler := api.NewHandler(instance)
	token := testenv.Token(t, instance)

	// 0) 界面提示用的存储信息：S3 后端要带上桶与前缀。
	storage := testenv.Do(handler, http.MethodGet, "/api/storage", token)
	if storage.Code != http.StatusOK {
		t.Fatalf("/api/storage 状态码 = %d", storage.Code)
	}
	storagePayload := testenv.Decode(t, storage)
	if storagePayload["backend"] != "s3" || storagePayload["bucket"] != cfg.Bucket ||
		storagePayload["prefix"] != strings.Trim(cfg.Prefix, "/") {
		t.Errorf("S3 存储信息 = %v", storagePayload)
	}

	content := []byte("s3 integration content")
	sha := testenv.SHA256(content)
	body, contentType := testenv.MultipartBody(t, "S3 测试.txt", content, sha, true)
	recorder := testenv.DoRaw(handler, http.MethodPost, "/api/upload", body, contentType, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("上传状态码 = %d，体=%s", recorder.Code, recorder.Body.String())
	}
	payload := testenv.Decode(t, recorder)
	id, _ := payload["id"].(string)
	if id == "" {
		t.Fatalf("上传响应里没有 id: %s", recorder.Body.String())
	}

	// 1) 对象按 <prefix>/<sha 前 2 位>/<sha 第 3-4 位>/<sha> 落在桶里。
	size, exists := testenv.S3Stat(t, cfg, sha)
	if !exists {
		t.Fatalf("对象 %s 没有出现在桶里", testenv.S3Key(cfg, sha))
	}
	if size != int64(len(content)) {
		t.Errorf("对象大小 = %d，想要 %d", size, len(content))
	}
	// 2) S3 模式下 files/ 里不该留下 blob（临时文件上传完成后删掉）。
	if entries, err := os.ReadDir(filepath.Join(instance.Config.DataDir, "files")); err != nil {
		t.Fatalf("读取 files 目录失败: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("files/ 里还剩 %d 个文件: %v", len(entries), entries)
	}

	// 3) 完整下载。
	download := testenv.Do(handler, http.MethodGet, "/api/files/"+id+"/download", token)
	if download.Code != http.StatusOK {
		t.Fatalf("下载状态码 = %d", download.Code)
	}
	if got := download.Body.String(); got != string(content) {
		t.Errorf("下载内容 = %q", got)
	}
	if got := download.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") || !strings.Contains(got, "filename*=") {
		t.Errorf("Content-Disposition = %q，非 ASCII 文件名应走 RFC 2231", got)
	}
	if got := download.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q", got)
	}

	// 4) Range 与 HEAD。
	ranged := testenv.DoRequest(handler, http.MethodGet, "/api/files/"+id+"/download",
		nil, "", token, map[string]string{"Range": "bytes=0-3"})
	if ranged.Code != http.StatusPartialContent {
		t.Fatalf("Range 状态码 = %d", ranged.Code)
	}
	if got := ranged.Body.String(); got != "s3 i" {
		t.Errorf("Range 响应体 = %q", got)
	}
	if got := ranged.Header().Get("Content-Range"); !strings.Contains(got, "bytes 0-3/") {
		t.Errorf("Content-Range = %q", got)
	}
	head := testenv.Do(handler, http.MethodHead, "/api/files/"+id+"/download", token)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD 状态码 = %d，体长 = %d", head.Code, head.Body.Len())
	}

	// 5) 秒传：同一内容补一条记录，两条记录共享一个对象。
	fields, fieldType := testenv.FieldBody(t, "client_sha256", sha, "filename", "copy.txt")
	check := testenv.DoRaw(handler, http.MethodPost, "/api/check_file", fields, fieldType, token)
	if check.Code != http.StatusOK {
		t.Fatalf("check_file 状态码 = %d，体=%s", check.Code, check.Body.String())
	}
	checkPayload := testenv.Decode(t, check)
	if got := checkPayload["exists"]; got != true {
		t.Fatalf("check_file exists = %v，想要 true", got)
	}
	instantID, _ := checkPayload["id"].(string)
	if instantID == "" {
		t.Fatal("秒传响应里没有 id")
	}

	// 6) 引用计数删除：删一条记录对象还在，删到没有记录才消失。
	if recorder := testenv.Do(handler, http.MethodDelete, "/api/files/"+id, token); recorder.Code != http.StatusNoContent {
		t.Fatalf("删除第一条状态码 = %d", recorder.Code)
	}
	if _, exists := testenv.S3Stat(t, cfg, sha); !exists {
		t.Fatal("还有记录引用时对象不该被删")
	}
	if recorder := testenv.Do(handler, http.MethodDelete, "/api/files/"+instantID, token); recorder.Code != http.StatusNoContent {
		t.Fatalf("删除第二条状态码 = %d", recorder.Code)
	}
	if _, exists := testenv.S3Stat(t, cfg, sha); exists {
		t.Fatal("最后一条记录删除后对象应当消失")
	}

	// 7) 记录在、对象丢失：下载按 404，不暴露存储后端状态。
	lost := testenv.SeedUpload(t, instance, "lost.txt", []byte("lost content"))
	if err := instance.Blobs.Delete(t.Context(), lost.SHA256); err != nil {
		t.Fatalf("删除对象失败: %v", err)
	}
	missing := testenv.Do(handler, http.MethodGet, "/api/files/"+lost.ID+"/download", token)
	assertJSONError(t, missing, http.StatusNotFound, "找不到该文件")
}

// TestS3MultipartUpload 用超过分片阈值（16 MiB）的内容走一遍 S3 多段上传：
// 上传后的对象能被完整读回，Range 也能跨分片边界。
func TestS3MultipartUpload(t *testing.T) {
	instance, cfg := testenv.NewS3With(t, func(c *app.Config) {
		c.MaxFileSize = 64 << 20
	})
	handler := api.NewHandler(instance)
	token := testenv.Token(t, instance)

	content := make([]byte, 20<<20)
	for i := range content {
		content[i] = byte(i * 31)
	}
	sha := testenv.SHA256(content)
	body, contentType := testenv.MultipartBody(t, "big.bin", content, sha, true)
	recorder := testenv.DoRaw(handler, http.MethodPost, "/api/upload", body, contentType, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("上传状态码 = %d，体=%s", recorder.Code, recorder.Body.String())
	}
	id, _ := testenv.Decode(t, recorder)["id"].(string)
	size, exists := testenv.S3Stat(t, cfg, sha)
	if !exists || size != int64(len(content)) {
		t.Fatalf("对象 = (%d, %v)，想要 %d 字节", size, exists, len(content))
	}

	download := testenv.Do(handler, http.MethodGet, "/api/files/"+id+"/download", token)
	if download.Code != http.StatusOK {
		t.Fatalf("下载状态码 = %d", download.Code)
	}
	if got := testenv.SHA256(download.Body.Bytes()); got != sha {
		t.Errorf("下载内容的 sha256 = %s，想要 %s", got, sha)
	}

	// 跨 16 MiB 分片边界的 Range：取 [16MiB-2, 16MiB+1] 三个字节。
	ranged := testenv.DoRequest(handler, http.MethodGet, "/api/files/"+id+"/download",
		nil, "", token, map[string]string{"Range": "bytes=16777214-16777216"})
	if ranged.Code != http.StatusPartialContent {
		t.Fatalf("Range 状态码 = %d", ranged.Code)
	}
	want := content[16777214:16777217]
	if got := ranged.Body.Bytes(); string(got) != string(want) {
		t.Errorf("跨分片 Range 内容不对")
	}
}
