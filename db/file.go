package db

import (
	"database/sql"
	"errors"
)

// ErrNotFound 表示记录不存在；调用方据此回 404，而不是 500。
var ErrNotFound = errors.New("file not found")

// File 是 file 表的一行。
type File struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	IsFavorite bool   `json:"is_favorite"`
	IsPublic   bool   `json:"is_public"`
	CreatedAt  int64  `json:"created_at"`
}

const fileColumns = "id, filename, sha256, size, is_favorite, is_public, created_at"

// rowScanner 让单行查询与多行查询共用一套扫描代码。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanFile(row rowScanner) (*File, error) {
	var file File
	if err := row.Scan(&file.ID, &file.Filename, &file.SHA256, &file.Size,
		&file.IsFavorite, &file.IsPublic, &file.CreatedAt); err != nil {
		return nil, err
	}
	return &file, nil
}

// ListFiles 按创建时间倒序返回全部记录；同一秒创建的按 id 升序，保证排序稳定。
func (s *Store) ListFiles() ([]File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT " + fileColumns + " FROM file ORDER BY created_at DESC, id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []File{}
	for rows.Next() {
		file, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, *file)
	}
	return files, rows.Err()
}

// File 按 id 取一条记录，不存在返回 ErrNotFound。
func (s *Store) File(id string) (*File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := scanFile(s.db.QueryRow("SELECT "+fileColumns+" FROM file WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return file, nil
}

// PublicFile 取一条公开记录：私有与不存在一样返回 ErrNotFound，调用方不用（也不该）区分。
func (s *Store) PublicFile(id string) (*File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := scanFile(s.db.QueryRow("SELECT "+fileColumns+" FROM file WHERE id = ? AND is_public = 1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return file, nil
}

// InsertFile 插入一条记录。id 重复属于程序 bug，直接让错误往上抛。
func (s *Store) InsertFile(file File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		"INSERT INTO file ("+fileColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		file.ID, file.Filename, file.SHA256, file.Size, file.IsFavorite, file.IsPublic, file.CreatedAt)
	return err
}

// ToggleFavorite 翻转收藏状态并返回翻转后的值；记录不存在返回 ErrNotFound。
func (s *Store) ToggleFavorite(id string) (bool, error) {
	return s.toggle(id, "is_favorite")
}

// TogglePublic 翻转公开状态并返回翻转后的值；记录不存在返回 ErrNotFound。
func (s *Store) TogglePublic(id string) (bool, error) {
	return s.toggle(id, "is_public")
}

// toggle 是两种开关的共同实现。列名只来自本包内的两个常量，不存在注入面。
func (s *Store) toggle(id, column string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE file SET "+column+" = 1 - "+column+" WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, ErrNotFound
	}
	var value bool
	if err := tx.QueryRow("SELECT "+column+" FROM file WHERE id = ?", id).Scan(&value); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return value, nil
}

// DeleteFile 删除一条记录；不存在返回 ErrNotFound。blob 的引用计数由调用方决定怎么处理。
func (s *Store) DeleteFile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("DELETE FROM file WHERE id = ?", id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// CountBySHA 数某个 blob 还有多少条记录引用它，删除记录后用来决定 blob 是否还要保留。
func (s *Store) CountBySHA(sha string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM file WHERE sha256 = ?", sha).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
