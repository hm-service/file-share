package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTightenPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if err := TightenPerm(path, SecretPerm); err != nil {
		t.Fatalf("TightenPerm() 报错: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if info.Mode().Perm() != SecretPerm {
		t.Errorf("权限 = %04o，想要 %04o", info.Mode().Perm(), SecretPerm)
	}
}

func TestTightenPermMissingPathIsFine(t *testing.T) {
	if err := TightenPerm(filepath.Join(t.TempDir(), "absent"), DataDirPerm); err != nil {
		t.Errorf("路径不存在时 TightenPerm 不该报错: %v", err)
	}
}

func TestCheckPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := CheckPerm(path, DataDirPerm); err == nil {
		t.Error("权限不对时 CheckPerm 应当报错")
	}
	if err := os.Chmod(path, DataDirPerm); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	if err := CheckPerm(path, DataDirPerm); err != nil {
		t.Errorf("权限正确时 CheckPerm 报错: %v", err)
	}
	if err := CheckPerm(filepath.Join(dir, "absent"), DataDirPerm); err == nil {
		t.Error("路径不存在时 CheckPerm 应当报错")
	}
}
