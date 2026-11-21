package app_test

import (
	"strings"
	"testing"

	"file-share/app"
	"file-share/internal/testenv"
)

// TestS3StartupFailsOnMissingBucket：桶不存在时启动必须当场失败，而不是等到第一次上传。
func TestS3StartupFailsOnMissingBucket(t *testing.T) {
	cfg := testenv.S3Config(t)
	cfg.Bucket = "file-share-test-missing-bucket"
	cfg.Prefix = "file-share-test/startup"
	_, err := app.Open(app.Config{
		DataDir:        t.TempDir(),
		TrustedProxies: "127.0.0.1/32",
		MaxFileSize:    1 << 20,
		S3:             cfg,
	})
	if err == nil {
		t.Fatal("桶不存在时 app.Open 应当失败")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("错误里应当提到 bucket: %v", err)
	}
}
