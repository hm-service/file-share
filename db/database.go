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
// 对不上的一律当作旧数据要求清空重来，不写迁移。
const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS file (
    id          TEXT    PRIMARY KEY,   -- uuid4，32 位小写十六进制无连字符
    filename    TEXT    NOT NULL,      -- 展示 / 下载用文件名（仅 basename，<=255 字节）
    sha256      TEXT    NOT NULL,      -- 64 位小写十六进制
    size        INTEGER NOT NULL,      -- 字节数
    is_favorite INTEGER NOT NULL DEFAULT 0,
    is_public   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL       -- unix 秒
);

CREATE INDEX IF NOT EXISTS file_sha256_idx ON file (sha256);
`

// Store 包住 database/sql，并在多步写操作上加一把锁（SQLite 单写者，连接也只有一个）。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开（必要时创建）数据库。新库建表并打上 schema 版本；已有的库版本对不上就报错，
// 交给启动流程退出 —— 旧数据不做迁移。
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
		if version != schemaVersion {
			handle.Close()
			return nil, fmt.Errorf("database schema version is %d, want %d: remove DATA_DIR and start over", version, schemaVersion)
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
