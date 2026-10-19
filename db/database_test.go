package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "file-share.db"))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestOpenFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "file-share.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	defer store.Close()

	version, err := store.schemaVersion()
	if err != nil {
		t.Fatalf("读 schema 版本失败: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("schema 版本 = %d，想要 %d", version, schemaVersion)
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("读 journal_mode 失败: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Errorf("journal_mode = %q，想要 wal", journalMode)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("读 busy_timeout 失败: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d，想要 5000", busyTimeout)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 库文件失败: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("库文件权限 = %04o，想要 0600", info.Mode().Perm())
	}
}

// 已有的库版本对不上：不迁移、不猜，直接报错让启动流程退出。
func TestOpenRejectsOldSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("改 schema 版本失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 报错: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("旧版本数据库应当打不开")
	} else if !strings.Contains(err.Error(), "schema version") {
		t.Errorf("错误信息 = %v，应当提示 schema 版本不对", err)
	}
}

// 零字节的旧文件（比如手工 touch 出来的）同样版本对不上。
func TestOpenRejectsEmptyExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("准备空文件失败: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("零字节库文件应当打不开")
	}
}

// 旧库权限被人改松过：启动时收紧。
func TestOpenTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 报错: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	defer reopened.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("权限 = %04o，想要 0600", info.Mode().Perm())
	}
}

func TestReady(t *testing.T) {
	store := testStore(t)
	if err := store.Ready(); err != nil {
		t.Fatalf("Ready() 报错: %v", err)
	}
	// 探针表不能留下来影响业务。
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '__probe'").Scan(&count); err != nil {
		t.Fatalf("查询探针表失败: %v", err)
	}
	if count != 0 {
		t.Error("Ready() 结束后 __probe 表应当被删掉")
	}
}
