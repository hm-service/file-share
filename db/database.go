// Package db 是 SQLite 存储层：单个库文件、单连接、无外键，一致性由代码保证。
// SQL 只出现在这个包里。
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"file-share/core"
)

// schemaVersion 是库结构的版本号，写在 PRAGMA user_version 里。
// 旧版本会在启动时一次性迁移到当前版本，其它版本一律要求清空重来。
const schemaVersion = 3

const schema = `
CREATE TABLE IF NOT EXISTS file (
    id          TEXT    PRIMARY KEY,   -- uuid4，32 位小写十六进制无连字符
    filename    TEXT    NOT NULL,      -- 展示 / 下载用文件名（仅 basename，<=255 字节）
    sha256      TEXT    NOT NULL,      -- 64 位小写十六进制
    size        INTEGER NOT NULL,      -- 字节数
    is_favorite INTEGER NOT NULL DEFAULT 0,
    is_public   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,      -- unix 秒
    group_name  TEXT    NOT NULL DEFAULT '', -- 一级分组名，空串表示未分组
    sort_order  INTEGER NOT NULL DEFAULT 0   -- 升序，越小越靠前
);

CREATE INDEX IF NOT EXISTS file_sha256_idx ON file (sha256);

CREATE TABLE IF NOT EXISTS group_order (
    name       TEXT    PRIMARY KEY,   -- 当前有文件的分组名（空分组不落库）
    sort_order INTEGER NOT NULL       -- 升序，越小越靠前
);
`

// Store 包住 database/sql，并在多步写操作上加一把锁（SQLite 单写者，连接也只有一个）。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开（必要时创建）数据库。新库直接建当前 schema；已有的旧库按版本逐步迁移到当前版本；
// 其它版本对不上就报错，交给启动流程退出 —— 不存在其它兼容分支。
func Open(path string) (*Store, error) {
	_, statErr := os.Stat(path)
	fresh := errors.Is(statErr, fs.ErrNotExist)
	if statErr != nil && !fresh {
		return nil, statErr
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, core.DataDirPerm); err != nil {
			return nil, err
		}
	}
	if fresh {
		// 先把库文件按私密权限建出来：SQLite 的 -wal / -shm 会沿用这个文件的权限。
		if err := createPrivateFile(path); err != nil {
			return nil, err
		}
	} else if err := core.TightenPerm(path, core.SecretPerm); err != nil {
		return nil, err
	}

	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单连接串行化所有访问：SQLite 同一时刻只有一个写者，事务与外键都交给代码保证。
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	store := &Store{db: handle}
	if fresh {
		if err := store.createSchema(); err != nil {
			handle.Close()
			return nil, err
		}
	} else {
		version, err := store.schemaVersion()
		if err != nil {
			handle.Close()
			return nil, err
		}
		switch version {
		case schemaVersion:
		case 1:
			if err := store.migrateV1ToV2(); err != nil {
				handle.Close()
				return nil, fmt.Errorf("migrate database v1 to v2: %w", err)
			}
			fallthrough
		case 2:
			if err := store.migrateV2ToV3(); err != nil {
				handle.Close()
				return nil, fmt.Errorf("migrate database to v%d: %w", schemaVersion, err)
			}
		default:
			handle.Close()
			return nil, fmt.Errorf("database schema version is %d, want %d (or 1/2 to migrate): remove DATA_DIR and start over", version, schemaVersion)
		}
	}
	// 建表之后 -wal / -shm 才出现，一并收紧（老库也顺带修正）。
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := core.TightenPerm(name, core.SecretPerm); err != nil {
			handle.Close()
			return nil, err
		}
	}
	return store, nil
}

func createPrivateFile(path string) error {
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, core.SecretPerm)
	if err != nil {
		return err
	}
	if err := handle.Close(); err != nil {
		return err
	}
	return core.TightenPerm(path, core.SecretPerm)
}

func (s *Store) createSchema() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// migrateV1ToV2 给 v1 库补上分组与排序两列。SQLite 的 DDL 是事务性的，
// 两条 ALTER 与 user_version 的更新放在同一个事务里：中途失败整体回滚，
// 不会留下「只加了一列」的半截库让下次启动再次迁移时撞上 duplicate column。
func (s *Store) migrateV1ToV2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("ALTER TABLE file ADD COLUMN group_name TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE file ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV2ToV3 建 group_order 表，并按分组名的字典序给已有分组补一份初始顺序。
// 分组顺序此前只是前端现算的展示顺序，这里把它落库；之后用户拖拽的结果才不会被覆盖。
func (s *Store) migrateV2ToV3() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS group_order (
		name       TEXT    PRIMARY KEY,
		sort_order INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT DISTINCT group_name FROM file WHERE group_name <> '' ORDER BY group_name")
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i, name := range names {
		if _, err := tx.Exec("INSERT INTO group_order (name, sort_order) VALUES (?, ?)", name, i); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) schemaVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ready 是 /readyz 的数据库部分：建 / 删探针表证明可读可写。探针表不在业务表里，
// schema 版本也不看它，因此不会干扰数据。
func (s *Store) Ready() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec("DROP TABLE IF EXISTS __probe"); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE TABLE __probe (id INTEGER)"); err != nil {
		return err
	}
	if _, err := s.db.Exec("DROP TABLE __probe"); err != nil {
		return err
	}
	return nil
}
