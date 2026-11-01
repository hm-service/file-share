package db

import (
	"database/sql"
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
	var newColumns int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('file') WHERE name IN ('group_name', 'sort_order')").Scan(&newColumns); err != nil {
		t.Fatalf("读表结构失败: %v", err)
	}
	if newColumns != 2 {
		t.Errorf("新库里 group_name / sort_order 两列只找到 %d 列", newColumns)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 库文件失败: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("库文件权限 = %04o，想要 0600", info.Mode().Perm())
	}
}

// 版本既不是当前版本也不是可迁移的 v1：不猜、直接报错让启动流程退出。
func TestOpenRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 3"); err != nil {
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

// createV1Database 手工造一个 v1 库：schema v1、两条记录、user_version = 1，
// 用来验证启动时的一次性迁移。
func createV1Database(t *testing.T, path string) {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v1 库失败: %v", err)
	}
	defer handle.Close()
	statements := []string{
		`CREATE TABLE file (
			id          TEXT    PRIMARY KEY,
			filename    TEXT    NOT NULL,
			sha256      TEXT    NOT NULL,
			size        INTEGER NOT NULL,
			is_favorite INTEGER NOT NULL DEFAULT 0,
			is_public   INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX file_sha256_idx ON file (sha256)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at)
		 VALUES ('old-one', 'one.txt', 'sha-one', 3, 0, 0, 100)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at)
		 VALUES ('old-two', 'two.txt', 'sha-two', 4, 1, 1, 200)`,
		`PRAGMA user_version = 1`,
	}
	for _, statement := range statements {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatalf("准备 v1 库失败: %v", err)
		}
	}
}

// v1 → v2 迁移：老数据保留、新列拿到默认值、旧顺序不变（sort_order 全 0 时按 created_at 倒序），
// 迁移后插入从 max+1 开始，再启动一次不会重复迁移。
func TestOpenMigratesV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	createV1Database(t, path)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 迁移报错: %v", err)
	}
	version, err := store.schemaVersion()
	if err != nil || version != schemaVersion {
		t.Fatalf("迁移后 schema 版本 = %d, %v，想要 %d", version, err, schemaVersion)
	}
	var columns int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('file') WHERE name IN ('group_name', 'sort_order')").Scan(&columns); err != nil {
		t.Fatalf("读表结构失败: %v", err)
	}
	if columns != 2 {
		t.Fatalf("迁移后 group_name / sort_order 两列只找到 %d 列", columns)
	}

	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if len(files) != 2 || files[0].ID != "old-two" || files[1].ID != "old-one" {
		t.Fatalf("迁移后列表 = %+v，想要旧数据保留且按 created_at 倒序", files)
	}
	for _, file := range files {
		if file.GroupName != "" || file.SortOrder != 0 {
			t.Errorf("迁移来的记录新列默认值不对: %+v", file)
		}
	}

	if err := store.InsertFile(sampleFile("new-one", "sha-new", 300)); err != nil {
		t.Fatalf("迁移后 InsertFile() 报错: %v", err)
	}
	if got, err := store.File("new-one"); err != nil || got.SortOrder != 1 {
		t.Fatalf("迁移后新记录 sort_order = %+v, %v，想要 1", got, err)
	}
	// 旧记录的 sort_order 是 0，迁移后插入的新记录是 max+1 = 1，所以排在旧记录之后。
	if files, err := store.ListFiles(); err != nil || len(files) != 3 || files[2].ID != "new-one" {
		t.Fatalf("迁移后列表 = %+v, %v，想要新记录排在最后", files, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 报错: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("第二次 Open() 报错: %v", err)
	}
	defer reopened.Close()
	if version, err := reopened.schemaVersion(); err != nil || version != schemaVersion {
		t.Fatalf("再次打开后 schema 版本 = %d, %v", version, err)
	}
	if got, err := reopened.File("new-one"); err != nil || got.SortOrder != 1 {
		t.Fatalf("再次打开后数据丢失: %+v, %v", got, err)
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
