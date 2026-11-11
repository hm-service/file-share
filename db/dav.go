package db

import (
	"database/sql"
	"errors"

	"file-share/core"
)

// ErrDavExists 表示 WebDAV 目标路径已经被占用：调用方据此回 409 / 405。
var ErrDavExists = errors.New("dav path exists")

// DavNode 是 WebDAV 命名空间里的一个节点（目录或文件）。
//
// WebDAV 有自己的一张表，和 file 表互不可见；两张表的内容共用同一个内容寻址 blob 池，
// 引用计数由 CountBySHA 跨表相加。
type DavNode struct {
	Path      string `json:"path"`   // 内部路径，空串是根；形如 "a/b/c"
	Parent    string `json:"parent"` // 父路径，根下为 ""
	Name      string `json:"name"`   // 路径最后一段
	IsDir     bool   `json:"is_dir"`
	SHA256    string `json:"sha256,omitempty"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const davColumns = "path, parent, name, is_dir, sha256, size, created_at, updated_at"

func scanDavNode(row interface{ Scan(dest ...any) error }) (*DavNode, error) {
	var (
		node  DavNode
		isDir int
	)
	if err := row.Scan(&node.Path, &node.Parent, &node.Name, &isDir,
		&node.SHA256, &node.Size, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return nil, err
	}
	node.IsDir = isDir == 1
	return &node, nil
}

// DavNode 按路径取一个节点；不存在返回 ErrNotFound。
func (s *Store) DavNode(path string) (*DavNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, err := scanDavNode(s.db.QueryRow("SELECT "+davColumns+" FROM dav_node WHERE path = ?", path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return node, err
}

// DavChildren 列出某个目录下的直接子节点：目录在前，其余按名字升序。
func (s *Store) DavChildren(parent string) ([]DavNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT "+davColumns+" FROM dav_node WHERE parent = ? ORDER BY is_dir DESC, name ASC", parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := []DavNode{}
	for rows.Next() {
		node, err := scanDavNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	}
	return nodes, rows.Err()
}

// InsertDavNode 插入一个节点；同一路径已经存在返回 ErrDavExists。
func (s *Store) InsertDavNode(node DavNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var exists int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM dav_node WHERE path = ?", node.Path).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrDavExists
	}
	_, err := s.db.Exec("INSERT INTO dav_node (path, parent, name, is_dir, sha256, size, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		node.Path, node.Parent, node.Name, boolInt(node.IsDir), node.SHA256, node.Size, node.CreatedAt, node.UpdatedAt)
	return err
}

// UpdateDavFile 改写一个文件节点的内容与修改时间；节点不存在返回 ErrNotFound。
func (s *Store) UpdateDavFile(path, sha string, size, updatedAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("UPDATE dav_node SET sha256 = ?, size = ?, updated_at = ? WHERE path = ? AND is_dir = 0",
		sha, size, updatedAt, path)
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

// DeleteDavSubtree 删除一个节点以及它的全部子节点，返回被删文件记录的 sha256
// （由调用方决定物理文件是否还有引用）。节点不存在返回 ErrNotFound。
func (s *Store) DeleteDavSubtree(path string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM dav_node WHERE path = ?", path).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}

	// 子树判断用前缀匹配而不是 LIKE：路径里可能带 % 或 _，那是 LIKE 的通配符。
	rows, err := tx.Query(`SELECT sha256 FROM dav_node
		WHERE is_dir = 0 AND (path = ? OR substr(path, 1, length(?) + 1) = ? || '/')`,
		path, path, path)
	if err != nil {
		return nil, err
	}
	shas := []string{}
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

	if _, err := tx.Exec(`DELETE FROM dav_node
		WHERE path = ? OR substr(path, 1, length(?) + 1) = ? || '/'`, path, path, path); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return shas, nil
}

// MoveDavNode 把 oldPath 改名 / 移到 newPath：目录连同整个子树一起改前缀。
// 源不存在返回 ErrNotFound，目标已存在返回 ErrDavExists。
func (s *Store) MoveDavNode(oldPath, newPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var isDir int
	if err := tx.QueryRow("SELECT is_dir FROM dav_node WHERE path = ?", oldPath).Scan(&isDir); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM dav_node WHERE path = ?", newPath).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrDavExists
	}

	if isDir == 1 {
		// 先整体改子树的前缀：子树里的行都以 oldPath + "/" 开头，根节点自己不匹配。
		if _, err := tx.Exec(`UPDATE dav_node
			SET path = ? || substr(path, length(?) + 1)
			WHERE substr(path, 1, length(?) + 1) = ? || '/'`,
			newPath, oldPath, oldPath, oldPath); err != nil {
			return err
		}
	}
	parent, name := core.SplitPath(newPath)
	if _, err := tx.Exec("UPDATE dav_node SET path = ?, parent = ?, name = ? WHERE path = ?",
		newPath, parent, name, oldPath); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}