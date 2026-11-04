package db

import (
	"database/sql"
	"errors"

	"file-share/core"
)

// ErrNotFound 表示记录不存在；调用方据此回 404，而不是 500。
var ErrNotFound = errors.New("file not found")

// ErrGroupExists 表示目标分组名已经被别的文件占用：重命名只改一个组的名字，不做隐式合并。
var ErrGroupExists = errors.New("group already exists")

// ErrGroupOrderMismatch 表示提交的分组顺序与当前实际分组对不上（分组被增删或改名了）。
var ErrGroupOrderMismatch = errors.New("group order does not match current groups")

// File 是 file 表的一行。
type File struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	IsFavorite bool   `json:"is_favorite"`
	IsPublic   bool   `json:"is_public"`
	CreatedAt  int64  `json:"created_at"`
	GroupName  string `json:"group_name"`
	SortOrder  int64  `json:"sort_order"`
}

const fileColumns = "id, filename, sha256, size, is_favorite, is_public, created_at, group_name, sort_order"

// rowScanner 让单行查询与多行查询共用一套扫描代码。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanFile(row rowScanner) (*File, error) {
	var file File
	if err := row.Scan(&file.ID, &file.Filename, &file.SHA256, &file.Size,
		&file.IsFavorite, &file.IsPublic, &file.CreatedAt, &file.GroupName, &file.SortOrder); err != nil {
		return nil, err
	}
	return &file, nil
}

// ListFiles 按用户排定的顺序返回全部记录：sort_order 升序，其次创建时间倒序、
// id 升序 —— 后两级保证迁移来的旧数据（sort_order 全 0）仍是原来的顺序，且结果稳定。
func (s *Store) ListFiles() ([]File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT " + fileColumns + " FROM file ORDER BY sort_order ASC, created_at DESC, id ASC")
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

// InsertFile 插入一条记录并把它排在列表末尾：sort_order 取当前最大值 +1。
// 上传与秒传都走这里，所以新文件默认出现在最后；id 重复属于程序 bug，直接让错误往上抛。
func (s *Store) InsertFile(file File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), 0) + 1 FROM file").Scan(&file.SortOrder); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"INSERT INTO file ("+fileColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		file.ID, file.Filename, file.SHA256, file.Size, file.IsFavorite, file.IsPublic,
		file.CreatedAt, file.GroupName, file.SortOrder); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateFile 按需改写一条记录的文件名与分组名：指针为 nil 表示不动这个字段。
//
// 改分组时把记录移到列表末尾（sort_order 取最大值 +1）；只改名时顺序不变。
// 改名与移动分组要么一起生效、要么一起回滚，所以放在一个事务里。
func (s *Store) UpdateFile(id string, filename, groupName *string) (*File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if filename == nil && groupName == nil {
		return nil, errors.New("UpdateFile requires at least one field")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	changed := int64(0)
	oldGroup := ""
	if groupName != nil {
		if err := tx.QueryRow("SELECT group_name FROM file WHERE id = ?", id).Scan(&oldGroup); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		var next int64
		if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), 0) + 1 FROM file").Scan(&next); err != nil {
			return nil, err
		}
		result, err := tx.Exec("UPDATE file SET group_name = ?, sort_order = ? WHERE id = ?", *groupName, next, id)
		if err != nil {
			return nil, err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return nil, err
		}
	}
	if filename != nil {
		result, err := tx.Exec("UPDATE file SET filename = ? WHERE id = ?", *filename, id)
		if err != nil {
			return nil, err
		}
		// 只改名时以这次更新的行数判断记录是否存在；两个字段一起改时
		// 分组更新已经确认过存在性，这里不必重复判断。
		if changed == 0 {
			changed, err = result.RowsAffected()
			if err != nil {
				return nil, err
			}
		}
	}
	if changed == 0 {
		return nil, ErrNotFound
	}
	if groupName != nil && *groupName != oldGroup {
		if err := ensureGroupOrder(tx, *groupName); err != nil {
			return nil, err
		}
		if err := cleanupGroupOrder(tx, oldGroup); err != nil {
			return nil, err
		}
	}

	file, err := scanFile(tx.QueryRow("SELECT "+fileColumns+" FROM file WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return file, nil
}

// SetOrder 在一个事务里重写一批记录的顺序；任何一条记录不存在就整体回滚并返回 ErrNotFound，
// 批量排序要么全部生效、要么一条都不动。
func (s *Store) SetOrder(items []core.OrderItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		result, err := tx.Exec("UPDATE file SET sort_order = ? WHERE id = ?", item.SortOrder, item.ID)
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
	}
	return tx.Commit()
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
// 如果删掉的是某个分组的最后一个文件，同时清掉该分组的顺序行。
func (s *Store) DeleteFile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var group string
	if err := tx.QueryRow("SELECT group_name FROM file WHERE id = ?", id).Scan(&group); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec("DELETE FROM file WHERE id = ?", id); err != nil {
		return err
	}
	if err := cleanupGroupOrder(tx, group); err != nil {
		return err
	}
	return tx.Commit()
}

// RenameGroup 把 oldName 分组下的全部记录改名为 newName，保持各记录的 sort_order 不变
// （组内顺序不变）。源分组不存在返回 ErrNotFound，目标分组已被占用返回 ErrGroupExists。
func (s *Store) RenameGroup(oldName, newName string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var conflicts int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM file WHERE group_name = ? AND group_name <> ?",
		newName, oldName,
	).Scan(&conflicts); err != nil {
		return 0, err
	}
	if conflicts > 0 {
		return 0, ErrGroupExists
	}

	result, err := tx.Exec("UPDATE file SET group_name = ? WHERE group_name = ?", newName, oldName)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed == 0 {
		return 0, ErrNotFound
	}
	if oldName != newName {
		result, err := tx.Exec("UPDATE OR REPLACE group_order SET name = ? WHERE name = ?", newName, oldName)
		if err != nil {
			return 0, err
		}
		orderChanged, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if orderChanged == 0 {
			// 旧分组没有顺序行（理论上不会发生）：给新名字补一条到末尾。
			if err := ensureGroupOrder(tx, newName); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

// Groups 返回当前有文件的分组名，按 group_order 升序；没有顺序行的分组排在最后（理论上不会出现）。
func (s *Store) Groups() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`
		SELECT f.name
		FROM (SELECT DISTINCT group_name AS name FROM file WHERE group_name <> '') AS f
		LEFT JOIN group_order AS g ON g.name = f.name
		ORDER BY COALESCE(g.sort_order, 9223372036854775807) ASC, f.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// SetGroupOrder 按给定顺序重写全部分组的 sort_order。names 必须与当前实际分组集合完全一致，
// 否则返回 ErrGroupOrderMismatch 且一条都不改。
func (s *Store) SetGroupOrder(names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query("SELECT DISTINCT group_name FROM file WHERE group_name <> ''")
	if err != nil {
		return err
	}
	current := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		current[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(current) != len(names) {
		return ErrGroupOrderMismatch
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := current[name]; !ok {
			return ErrGroupOrderMismatch
		}
		if _, dup := seen[name]; dup {
			return ErrGroupOrderMismatch
		}
		seen[name] = struct{}{}
	}

	for i, name := range names {
		if _, err := tx.Exec(
			"INSERT INTO group_order (name, sort_order) VALUES (?, ?) "+
				"ON CONFLICT(name) DO UPDATE SET sort_order = excluded.sort_order",
			name, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureGroupOrder 保证有文件的分组在 group_order 里有一行；新分组排在最后。
func ensureGroupOrder(tx *sql.Tx, name string) error {
	if name == "" {
		return nil
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM group_order WHERE name = ?", name).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	var next int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), -1) + 1 FROM group_order").Scan(&next); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO group_order (name, sort_order) VALUES (?, ?)", name, next)
	return err
}

// cleanupGroupOrder 在某个分组已经没有文件时删掉它的顺序行；空串（未分组）不是分组。
func cleanupGroupOrder(tx *sql.Tx, name string) error {
	if name == "" {
		return nil
	}
	_, err := tx.Exec(
		"DELETE FROM group_order WHERE name = ? AND NOT EXISTS (SELECT 1 FROM file WHERE group_name = ?)",
		name, name,
	)
	return err
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
