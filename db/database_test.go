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
		"SELECT COUNT(*) FROM pragma_table_info('file') WHERE name IN ('group_path', 'sort_order')").Scan(&newColumns); err != nil {
		t.Fatalf("读表结构失败: %v", err)
	}
	if newColumns != 2 {
		t.Errorf("新库里 group_path / sort_order 两列只找到 %d 列", newColumns)
	}
	var hasGroups int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'groups'").Scan(&hasGroups); err != nil {
		t.Fatalf("查询 groups 表失败: %v", err)
	}
	if hasGroups != 1 {
		t.Error("新库里应当有 groups 表")
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
	if _, err := store.db.Exec("PRAGMA user_version = 99"); err != nil {
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
		"SELECT COUNT(*) FROM pragma_table_info('file') WHERE name IN ('group_path', 'sort_order')").Scan(&columns); err != nil {
		t.Fatalf("读表结构失败: %v", err)
	}
	if columns != 2 {
		t.Fatalf("迁移后 group_path / sort_order 两列只找到 %d 列", columns)
	}
	var hasGroups int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'groups'").Scan(&hasGroups); err != nil {
		t.Fatalf("查询 groups 表失败: %v", err)
	}
	if hasGroups != 1 {
		t.Fatal("迁移后应当有 groups 表")
	}

	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if len(files) != 2 || files[0].ID != "old-two" || files[1].ID != "old-one" {
		t.Fatalf("迁移后列表 = %+v，想要旧数据保留且按 created_at 倒序", files)
	}
	for _, file := range files {
		if file.GroupPath != "" || file.SortOrder != 0 {
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

// createV2Database 造一个 v2 库（有分组但没有 group_order 表），用来验证 v2 → v3 迁移。
func createV2Database(t *testing.T, path string) {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v2 库失败: %v", err)
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
			created_at  INTEGER NOT NULL,
			group_name  TEXT    NOT NULL DEFAULT '',
			sort_order  INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX file_sha256_idx ON file (sha256)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at, group_name, sort_order)
		 VALUES ('two', 'two.txt', 'sha-two', 2, 0, 0, 200, 'b组', 0)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at, group_name, sort_order)
		 VALUES ('one', 'one.txt', 'sha-one', 1, 0, 0, 100, 'a组', 1)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at, group_name, sort_order)
		 VALUES ('none', 'none.txt', 'sha-none', 3, 0, 0, 300, '', 2)`,
		`PRAGMA user_version = 2`,
	}
	for _, statement := range statements {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatalf("准备 v2 库失败: %v", err)
		}
	}
}

// v2 → v3：建 group_order 表并按分组名给已有分组补初始顺序，数据与用户后续拖拽的顺序都保留。
func TestOpenMigratesV2Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	createV2Database(t, path)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 迁移报错: %v", err)
	}
	if version, err := store.schemaVersion(); err != nil || version != schemaVersion {
		t.Fatalf("迁移后 schema 版本 = %d, %v，想要 %d", version, err, schemaVersion)
	}
	groups, err := store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	if !samePaths(groupPaths(groups), "a组", "b组") {
		t.Fatalf("迁移后的分组顺序 = %v，想要 [a组 b组]", groupPaths(groups))
	}
	// 用户拖拽后的顺序要能持久化。
	if err := store.SetGroupOrder("", []string{"b组", "a组"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 报错: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("第二次 Open() 报错: %v", err)
	}
	defer reopened.Close()
	groups, err = reopened.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	if !samePaths(groupPaths(groups), "b组", "a组") {
		t.Fatalf("重开后分组顺序 = %v，想要 [b组 a组]", groupPaths(groups))
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

// createV3Database 造一个 v3 库：file + group_order + user_version = 3，验证 v3 → v4 迁移。
func createV3Database(t *testing.T, path string) {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v3 库失败: %v", err)
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
			created_at  INTEGER NOT NULL,
			group_name  TEXT    NOT NULL DEFAULT '',
			sort_order  INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX file_sha256_idx ON file (sha256)`,
		`CREATE TABLE group_order (name TEXT PRIMARY KEY, sort_order INTEGER NOT NULL)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at, group_name, sort_order)
		 VALUES ('one', 'one.txt', 'sha-one', 1, 0, 0, 100, '乙组', 0)`,
		`INSERT INTO group_order (name, sort_order) VALUES ('甲组', 1)`,
		`INSERT INTO group_order (name, sort_order) VALUES ('乙组', 0)`,
		`PRAGMA user_version = 3`,
	}
	for _, statement := range statements {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatalf("准备 v3 库失败: %v", err)
		}
	}
}

// createV4Database 造一个 v4 库：file + groups（一级分组）+ user_version = 4，验证 v4 → v5 迁移。
func createV4Database(t *testing.T, path string) {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v4 库失败: %v", err)
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
			created_at  INTEGER NOT NULL,
			group_name  TEXT    NOT NULL DEFAULT '',
			sort_order  INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX file_sha256_idx ON file (sha256)`,
		`CREATE TABLE groups (name TEXT PRIMARY KEY, sort_order INTEGER NOT NULL)`,
		`INSERT INTO file (id, filename, sha256, size, is_favorite, is_public, created_at, group_name, sort_order)
		 VALUES ('one', 'one.txt', 'sha-one', 1, 0, 0, 100, '乙组', 0)`,
		`INSERT INTO groups (name, sort_order) VALUES ('甲组', 1)`,
		`INSERT INTO groups (name, sort_order) VALUES ('乙组', 0)`,
		`PRAGMA user_version = 4`,
	}
	for _, statement := range statements {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatalf("准备 v4 库失败: %v", err)
		}
	}
}

// v4 → v5：一级分组补上 parent / name 两列，file.group_name 改名为 group_path；
// v4 的分组名不含 "/"，迁移后都变成顶级分组。
func TestOpenMigratesV4Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	createV4Database(t, path)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 迁移报错: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(); err != nil || version != schemaVersion {
		t.Fatalf("迁移后 schema 版本 = %d, %v，想要 %d", version, err, schemaVersion)
	}
	groups, err := store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	if !samePaths(groupPaths(groups), "乙组", "甲组") {
		t.Fatalf("迁移后的分组 = %v，想要 [乙组 甲组]", groupPaths(groups))
	}
	for _, group := range groups {
		if group.Parent != "" || group.Name != group.Path {
			t.Errorf("v4 分组迁移后 = %+v，想要顶级分组（parent 为空、name 等于路径）", group)
		}
	}
	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if len(files) != 1 || files[0].GroupPath != "乙组" {
		t.Fatalf("迁移后的文件 = %+v，想要 group_path 乙组", files)
	}

	// 迁移后可以直接建二级分组。
	if _, err := store.CreateGroup("乙组/子组"); err != nil {
		t.Fatalf("迁移后建二级分组报错: %v", err)
	}
}

// createV5Database 造一个 v5 库（两级分组已经就位，但没有 dav_node 表）。
func createV5Database(t *testing.T, path string) {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v5 库失败: %v", err)
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
			created_at  INTEGER NOT NULL,
			group_path  TEXT    NOT NULL DEFAULT '',
			sort_order  INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX file_sha256_idx ON file (sha256)`,
		`CREATE TABLE groups (
			path       TEXT    PRIMARY KEY,
			parent     TEXT    NOT NULL,
			name       TEXT    NOT NULL,
			sort_order INTEGER NOT NULL,
			UNIQUE (parent, name)
		)`,
		`INSERT INTO file (id, filename, sha256, size, created_at, group_path, sort_order)
		 VALUES ('old', 'old.txt', 'sha-old', 1, 100, '甲组', 0)`,
		`INSERT INTO groups (path, parent, name, sort_order) VALUES ('甲组', '', '甲组', 0)`,
		`PRAGMA user_version = 5`,
	}
	for _, statement := range statements {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatalf("准备 v5 库失败: %v", err)
		}
	}
}

// v5 → v6：只新增 dav_node 表，旧数据保持原样；引用计数开始跨表计算。
func TestOpenMigratesV5Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-share.db")
	createV5Database(t, path)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 迁移报错: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(); err != nil || version != schemaVersion {
		t.Fatalf("迁移后 schema 版本 = %d, %v，想要 %d", version, err, schemaVersion)
	}
	var hasDav int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'dav_node'").Scan(&hasDav); err != nil {
		t.Fatalf("查询 dav_node 表失败: %v", err)
	}
	if hasDav != 1 {
		t.Fatal("迁移后应当有 dav_node 表")
	}
	if files, err := store.ListFiles(); err != nil || len(files) != 1 || files[0].ID != "old" {
		t.Fatalf("迁移后旧记录 = %+v, %v", files, err)
	}
	if groups, err := store.Groups(); err != nil || len(groups) != 1 || groups[0].Path != "甲组" {
		t.Fatalf("迁移后旧分组 = %+v, %v", groups, err)
	}

	// 两张表引用同一个 sha 时，CountBySHA 要算两次。
	if err := store.InsertDavNode(DavNode{
		Path: "a.txt", Parent: "", Name: "a.txt", SHA256: "sha-old", Size: 1,
		CreatedAt: 200, UpdatedAt: 200,
	}); err != nil {
		t.Fatalf("迁移后 InsertDavNode() 报错: %v", err)
	}
	if count, err := store.CountBySHA("sha-old"); err != nil || count != 2 {
		t.Fatalf("CountBySHA(sha-old) = %d, %v，想要 2", count, err)
	}
	if _, err := store.DeleteDavSubtree("a.txt"); err != nil {
		t.Fatalf("DeleteDavSubtree() 报错: %v", err)
	}
	if count, err := store.CountBySHA("sha-old"); err != nil || count != 1 {
		t.Fatalf("删掉 dav 记录后 CountBySHA(sha-old) = %d, %v，想要 1", count, err)
	}
}
