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
	// 直接写入带分组的记录（迁移 / 测试）时也保证分组在 groups 表里有登记。
	if err := ensureGroup(tx, file.GroupName); err != nil {
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
	if groupName != nil {
		if err := ensureGroup(tx, *groupName); err != nil {
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
// 分组是独立注册的，删掉组内最后一个文件不会连带删除分组。
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

// RenameGroup 把分组与组内记录一起改名，组内 sort_order 不变；空分组也能改。
// 源分组不存在返回 ErrNotFound，目标分组已存在返回 ErrGroupExists（不做隐式合并）。
func (s *Store) RenameGroup(oldName, newName string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE name = ?", oldName).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	if oldName != newName {
		var conflicts int
		if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE name = ?", newName).Scan(&conflicts); err != nil {
			return 0, err
		}
		if conflicts > 0 {
			return 0, ErrGroupExists
		}
		if _, err := tx.Exec("UPDATE groups SET name = ? WHERE name = ?", newName, oldName); err != nil {
			return 0, err
		}
	}

	result, err := tx.Exec("UPDATE file SET group_name = ? WHERE group_name = ?", newName, oldName)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

// Groups 返回全部分组名（含空分组），按 sort_order 升序、名称兜底。
func (s *Store) Groups() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT name FROM groups ORDER BY sort_order ASC, name ASC")
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

// CreateGroup 新建一个空分组，追加到末尾；同名分组已存在返回 ErrGroupExists。
func (s *Store) CreateGroup(name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE name = ?", name).Scan(&exists); err != nil {
		return 0, err
	}
	if exists > 0 {
		return 0, ErrGroupExists
	}
	var next int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), -1) + 1 FROM groups").Scan(&next); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO groups (name, sort_order) VALUES (?, ?)", name, next); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
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

	rows, err := tx.Query("SELECT name FROM groups")
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
			"INSERT INTO groups (name, sort_order) VALUES (?, ?) "+
				"ON CONFLICT(name) DO UPDATE SET sort_order = excluded.sort_order",
			name, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureGroup 保证分组存在于 groups 表；没有就追加到末尾。空串（未分组）不落库。
func ensureGroup(tx *sql.Tx, name string) error {
	if name == "" {
		return nil
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE name = ?", name).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	var next int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), -1) + 1 FROM groups").Scan(&next); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO groups (name, sort_order) VALUES (?, ?)", name, next)
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
