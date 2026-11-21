package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"file-share/app"
	"file-share/internal/testenv"
)

func TestCheckStorageLocalBackend(t *testing.T) {
	instance := testenv.New(t)
	if err := instance.CheckStorage(context.Background()); err != nil {
		t.Fatalf("本地后端的存储探测应成功: %v", err)
	}
}

// blob 目录整个不见时不能报「文件不存在」：那是存储不可用（503），不是 404。
func TestLocalBlobStoreMissingDirIsNotNotFound(t *testing.T) {
	instance := testenv.New(t)
	file := testenv.SeedUpload(t, instance, "missing-dir.txt", []byte("missing dir"))
	if err := os.RemoveAll(filepath.Join(instance.Config.DataDir, "files")); err != nil {
		t.Fatalf("删除 blob 目录失败: %v", err)
	}
	if _, err := instance.Blobs.Head(context.Background(), file.SHA256); err == nil || errors.Is(err, app.ErrBlobNotFound) {
		t.Fatalf("blob 目录缺失时应报存储错误而不是 not-found，得到 %v", err)
	}
}
