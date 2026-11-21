package app_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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

// TestMigrateToS3 覆盖一次性迁移：dry-run 不写对象、正式迁移按 sha 去重上传、
// 可重复执行（已存在跳过）、不删除本地文件、迁移后的对象能被 S3 后端读回。
func TestMigrateToS3(t *testing.T) {
	dryCfg := testenv.S3Config(t)
	cfg := testenv.S3Config(t)
	local := testenv.New(t)

	alpha := testenv.SeedUpload(t, local, "alpha.txt", []byte("alpha content"))
	testenv.SeedUpload(t, local, "alpha-copy.txt", []byte("alpha content")) // 同内容第二条记录
	gamma := testenv.SeedUpload(t, local, "gamma.txt", []byte("gamma content"))
	want := []struct {
		sha  string
		size int64
	}{
		{alpha.SHA256, alpha.Size},
		{gamma.SHA256, gamma.Size},
	}

	// 1) dry-run：只打印，不写对象。
	var out bytes.Buffer
	if err := app.MigrateToS3(t.Context(), local.Config.DataDir, *dryCfg, true, &out); err != nil {
		t.Fatalf("dry-run 失败: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "summary: would upload 2, skipped 0, failed 0") {
		t.Errorf("dry-run 应当报告 2 个待上传对象:\n%s", out.String())
	}
	for _, item := range want {
		if _, exists := testenv.S3Stat(t, dryCfg, item.sha); exists {
			t.Errorf("dry-run 不该写出对象 %s", item.sha)
		}
	}

	// 2) 正式迁移：两个不同内容，两条同内容记录只传一份。
	out.Reset()
	if err := app.MigrateToS3(t.Context(), local.Config.DataDir, *cfg, false, &out); err != nil {
		t.Fatalf("迁移失败: %v\n%s", err, out.String())
	}
	for _, item := range want {
		size, exists := testenv.S3Stat(t, cfg, item.sha)
		if !exists {
			t.Fatalf("对象 %s 没迁移成功\n%s", item.sha, out.String())
		}
		if size != item.size {
			t.Errorf("对象 %s 大小 = %d，想要 %d", item.sha, size, item.size)
		}
	}
	if !strings.Contains(out.String(), "uploaded 2, skipped 0, failed 0") {
		t.Errorf("迁移汇总不对:\n%s", out.String())
	}

	// 3) 本地文件还在（回滚依赖它们）。
	if _, err := os.Stat(filepath.Join(local.Config.DataDir, "files", alpha.SHA256)); err != nil {
		t.Errorf("迁移不该删除本地 blob: %v", err)
	}

	// 4) 再跑一次：全部跳过，不重复上传。
	out.Reset()
	if err := app.MigrateToS3(t.Context(), local.Config.DataDir, *cfg, false, &out); err != nil {
		t.Fatalf("重复迁移失败: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "uploaded 0, skipped 2, failed 0") {
		t.Errorf("重复迁移应当全部跳过:\n%s", out.String())
	}

	// 5) 迁移后的对象能被 S3 后端读回。
	s3app := testenv.NewWith(t, func(c *app.Config) { c.S3 = cfg })
	blob, err := s3app.Blobs.Open(t.Context(), alpha.SHA256)
	if err != nil {
		t.Fatalf("Open 迁移后的对象失败: %v", err)
	}
	defer blob.Close()
	body, err := io.ReadAll(blob)
	if err != nil || string(body) != "alpha content" {
		t.Errorf("迁移后的内容 = %q err=%v", body, err)
	}
}

// TestMigrateToS3FailsOnMissingLocalBlob：本地 blob 缺失时必须报错并汇总失败数，
// 不能假装迁移成功。
func TestMigrateToS3FailsOnMissingLocalBlob(t *testing.T) {
	cfg := testenv.S3Config(t)
	local := testenv.New(t)
	record := testenv.SeedUpload(t, local, "gone.txt", []byte("gone content"))
	if err := os.Remove(filepath.Join(local.Config.DataDir, "files", record.SHA256)); err != nil {
		t.Fatalf("删除本地 blob 失败: %v", err)
	}
	var out bytes.Buffer
	err := app.MigrateToS3(t.Context(), local.Config.DataDir, *cfg, false, &out)
	if err == nil {
		t.Fatalf("本地 blob 缺失时迁移应当失败:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "local blob") || !strings.Contains(out.String(), "failed 1") {
		t.Errorf("输出里应当指明失败原因:\n%s", out.String())
	}
}
