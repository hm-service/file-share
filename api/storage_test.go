package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"file-share/api"
	"file-share/internal/testenv"
)

// failingBlobs 模拟存储后端不可达：探测与所有读写都返回同一个后端错误。
type failingBlobs struct{ err error }

func (f failingBlobs) Kind() string                                { return "s3" }
func (f failingBlobs) Check(context.Context) error                 { return f.err }
func (f failingBlobs) Head(context.Context, string) (int64, error) { return 0, f.err }
func (f failingBlobs) PutFile(context.Context, string, string, int64) error {
	return f.err
}
func (f failingBlobs) Open(context.Context, string) (io.ReadSeekCloser, error) {
	return nil, f.err
}
func (f failingBlobs) Delete(context.Context, string) error { return f.err }

// /api/storage 在存储后端可用时回 healthy=true。
func TestStorageHealthLocalBackend(t *testing.T) {
	instance := testenv.New(t)
	handler := api.NewHandler(instance)
	recorder := testenv.Do(handler, http.MethodGet, "/api/storage", testenv.Token(t, instance))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Backend string `json:"backend"`
		Healthy bool   `json:"healthy"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	if payload.Backend != "local" || !payload.Healthy {
		t.Fatalf("响应 = %+v，想要 local 且 healthy", payload)
	}
}

// 存储后端连不上时 /api/storage 回 healthy=false 并带错误文案，界面据此变红。
func TestStorageHealthReportsOutage(t *testing.T) {
	instance := testenv.New(t)
	instance.Blobs = failingBlobs{err: errors.New("dial tcp 10.8.0.202:8333: connect: connection refused")}
	handler := api.NewHandler(instance)
	recorder := testenv.Do(handler, http.MethodGet, "/api/storage", testenv.Token(t, instance))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Healthy bool   `json:"healthy"`
		Error   string `json:"error"`
	}
	testenv.DecodeInto(t, recorder, &payload)
	if payload.Healthy {
		t.Fatal("存储不可达时 healthy 应为 false")
	}
	if !strings.Contains(payload.Error, "存储服务暂时不可用") {
		t.Fatalf("error = %q，想要存储不可用文案", payload.Error)
	}
}

// 存储后端不可达时，签下载地址应回 503 而不是笼统的 500：前端才能提示稍后重试。
func TestDownloadURLStorageOutage(t *testing.T) {
	instance := testenv.New(t)
	file := testenv.SeedUpload(t, instance, "outage.txt", []byte("hello outage"))
	instance.Blobs = failingBlobs{err: errors.New("dial tcp 10.8.0.202:8333: connect: connection refused")}
	handler := api.NewHandler(instance)

	recorder := testenv.Do(handler, http.MethodGet, "/api/files/"+file.ID+"/download-url", testenv.Token(t, instance))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，想要 503（body=%s）", recorder.Code, recorder.Body.String())
	}
	if detail := detailOf(t, recorder); !strings.Contains(detail, "存储服务暂时不可用") {
		t.Fatalf("detail = %q，想要存储不可用文案", detail)
	}
}
