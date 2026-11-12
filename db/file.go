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
	CreatedAt  int64  `json:"created_at"`
	GroupPath  string `json:"group_path"`
	SortOrder  int64  `json:"sort_order"`
}

// Group 是 groups 表的一行：完整路径、父路径与最后一段名字。
// 展示顺序不放进 JSON —— 数组本身已经按父级分组、同级 sort_order 排好。
type Group struct {
	Path      string `json:"path"`
	Parent    string `json:"parent"`
	Name      string `json:"name"`
	SortOrder int64  `json:"-"`
}

const fileColumns = "id, filename, sha256, size, is_favorite, created_at, group_path, sort_order"

// rowScanner 让单行查询与多行查询共用一套扫描代码。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanFile(row rowScanner) (*File, error) {
	var file File
	if err := row.Scan(&file.ID, &file.Filename, &file.SHA256, &file.Size,
		&file.IsFavorite, &file.CreatedAt, &file.GroupPath, &file.SortOrder); err != nil {
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
		"INSERT INTO file ("+fileColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		file.ID, file.Filename, file.SHA256, file.Size, file.IsFavorite,
		file.CreatedAt, file.GroupPath, file.SortOrder); err != nil {
		return err
	}
	// 直接写入带分组的记录（迁移 / 测试）时也保证分组在 groups 表里有登记。
	if err := ensureGroup(tx, file.GroupPath); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateFile 按需改写一条记录的文件名与分组路径：指针为 nil 表示不动这个字段。
//
// 改分组时把记录移到列表末尾（sort_order 取最大值 +1）；只改名时顺序不变。
// 改名与移动分组要么一起生效、要么一起回滚，所以放在一个事务里。
func (s *Store) UpdateFile(id string, filename, groupPath *string) (*File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if filename == nil && groupPath == nil {
		return nil, errors.New("UpdateFile requires at least one field")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	changed := int64(0)
	if groupPath != nil {
		// 先确认记录存在：UPDATE 影响 0 行分不清「不存在」与「值没变」。
		var exists int
		if err := tx.QueryRow("SELECT 1 FROM file WHERE id = ?", id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		var next int64
		if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), 0) + 1 FROM file").Scan(&next); err != nil {
			return nil, err
		}
		result, err := tx.Exec("UPDATE file SET group_path = ?, sort_order = ? WHERE id = ?", *groupPath, next, id)
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
	if groupPath != nil {
		if err := ensureGroup(tx, *groupPath); err != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE file SET is_favorite = 1 - is_favorite WHERE id = ?", id)
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
	if err := tx.QueryRow("SELECT is_favorite FROM file WHERE id = ?", id).Scan(&value); err != nil {
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

// Groups 返回全部分组（含空分组）。顺序先按父路径、再按同级 sort_order：
// 调用方据此把同一父级的分组聚在一起，不用自己排序。
func (s *Store) Groups() ([]Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT path, parent, name, sort_order FROM groups ORDER BY parent ASC, sort_order ASC, name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var group Group
		if err := rows.Scan(&group.Path, &group.Parent, &group.Name, &group.SortOrder); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// CreateGroup 新建一个空分组，父级不存在时一并创建；路径已存在返回 ErrGroupExists。
// 新分组追加到同一父级的末尾。
func (s *Store) CreateGroup(path string) (*Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE path = ?", path).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, ErrGroupExists
	}
	if err := ensureGroup(tx, path); err != nil {
		return nil, err
	}
	group, err := scanGroup(tx.QueryRow(
		"SELECT path, parent, name, sort_order FROM groups WHERE path = ?", path))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return group, nil
}

// RenameGroup 只改写分组的最后一段名字，父路径与同级 sort_order 不变；
// 顶级分组的子分组与组内文件的路径一起跟着改。返回受影响的文件条数。
//
// 源分组不存在返回 ErrNotFound；目标路径已存在返回 ErrGroupExists（不做隐式合并）。
func (s *Store) RenameGroup(path, newName string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	parent, _ := core.SplitGroupPath(path)
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE path = ?", path).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	newPath := newName
	if parent != "" {
		newPath = parent + "/" + newName
	}
	if newPath != path {
		var conflicts int
		if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE path = ?", newPath).Scan(&conflicts); err != nil {
			return 0, err
		}
		if conflicts > 0 {
			return 0, ErrGroupExists
		}
	}

	changed := int64(0)
	// 先改自身，再改子分组：子分组的上限是一层，逐个改路径与父路径。
	if _, err := tx.Exec("UPDATE groups SET path = ?, name = ? WHERE path = ?", newPath, newName, path); err != nil {
		return 0, err
	}
	rows, err := tx.Query("SELECT name FROM groups WHERE parent = ?", path)
	if err != nil {
		return 0, err
	}
	var children []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		children = append(children, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, name := range children {
		if _, err := tx.Exec("UPDATE groups SET path = ?, parent = ? WHERE parent = ? AND name = ?",
			newPath+"/"+name, newPath, path, name); err != nil {
			return 0, err
		}
	}

	// 文件跟着路径走：先改直属，再逐个改子分组的。
	result, err := tx.Exec("UPDATE file SET group_path = ? WHERE group_path = ?", newPath, path)
	if err != nil {
		return 0, err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return 0, err
	}
	for _, name := range children {
		result, err := tx.Exec("UPDATE file SET group_path = ? WHERE group_path = ?",
			newPath+"/"+name, path+"/"+name)
		if err != nil {
			return 0, err
		}
		moved, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		changed += moved
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

// DeleteGroup 删除一个分组，它的子分组一并删除。
// deleteFiles 为 true 时同时删除组内（含子分组）全部记录并返回它们的 sha256（由调用方决定
// blob 是否还有引用）；否则把文件移到被删分组的父路径，顶级分组的文件回到未分组。
// 目标分组不存在返回 ErrNotFound。
func (s *Store) DeleteGroup(path string, deleteFiles bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE path = ?", path).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}

	// 受影响的路径：分组自己 + 它下面的子分组（最多一层）。
	targets := []string{path}
	rows, err := tx.Query("SELECT path FROM groups WHERE parent = ?", path)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, child)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	shas := []string{}
	if deleteFiles {
		for _, target := range targets {
			rows, err := tx.Query("SELECT sha256 FROM file WHERE group_path = ?", target)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var sha string
				if err := rows.Scan(&sha); err != nil {
					rows.Close()
					return nil, err
				}
				shas = append(shas, sha)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
		for _, target := range targets {
			if _, err := tx.Exec("DELETE FROM file WHERE group_path = ?", target); err != nil {
				return nil, err
			}
		}
	} else {
		// 文件上移到被删分组的父路径，并挪到目标分组的末尾。
		parent, _ := core.SplitGroupPath(path)
		var next int64
		if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM file").Scan(&next); err != nil {
			return nil, err
		}
		for _, target := range targets {
			ids := make([]string, 0)
			rows, err := tx.Query("SELECT id FROM file WHERE group_path = ? ORDER BY sort_order ASC, created_at DESC, id ASC", target)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
			for _, id := range ids {
				next++
				if _, err := tx.Exec("UPDATE file SET group_path = ?, sort_order = ? WHERE id = ?", parent, next, id); err != nil {
					return nil, err
				}
			}
		}
	}

	for _, target := range targets {
		if _, err := tx.Exec("DELETE FROM groups WHERE path = ?", target); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return shas, nil
}

// SetGroupOrder 按给定顺序重写某个父级下全部子分组的 sort_order。
// names 必须是该父级下全部子分组名（顶层用空串作父路径）；对不上返回 ErrGroupOrderMismatch。
func (s *Store) SetGroupOrder(parent string, names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query("SELECT name FROM groups WHERE parent = ?", parent)
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
	if len(current) != len(seen) {
		return ErrGroupOrderMismatch
	}

	for i, name := range names {
		path := name
		if parent != "" {
			path = parent + "/" + name
		}
		if _, err := tx.Exec("UPDATE groups SET sort_order = ? WHERE path = ?", i, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// scanGroup 读一行分组。
func scanGroup(row rowScanner) (*Group, error) {
	var group Group
	if err := row.Scan(&group.Path, &group.Parent, &group.Name, &group.SortOrder); err != nil {
		return nil, err
	}
	return &group, nil
}

// ensureGroup 保证分组及其祖先都存在；缺的追加到各自父级的末尾。空串（未分组）不落库。
func ensureGroup(tx *sql.Tx, path string) error {
	if path == "" {
		return nil
	}
	parent, name := core.SplitGroupPath(path)
	if parent != "" {
		if err := ensureGroup(tx, parent); err != nil {
			return err
		}
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM groups WHERE path = ?", path).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	var next int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), -1) + 1 FROM groups WHERE parent = ?", parent).Scan(&next); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO groups (path, parent, name, sort_order) VALUES (?, ?, ?, ?)",
		path, parent, name, next)
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
