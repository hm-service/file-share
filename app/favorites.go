package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// favoritesFileName 是收藏元数据文件名：放在 DATA_DIR 下（可写），不进只读数据目录。
const favoritesFileName = "favorites.json"

// favoritesPayload 是 favorites.json 的结构：只存相对路径列表。
type favoritesPayload struct {
	Paths []string `json:"paths"`
}

// Favorites 是收藏的相对路径集合，读写 DATA_DIR/favorites.json。
// 收藏是前端状态，与只读文件目录解耦：文件被 SMB 侧删掉后记录仍在，
// 读取时自然匹配不到文件、不会出现在「收藏」视图里。
type Favorites struct {
	path  string
	mu    sync.Mutex
	paths map[string]struct{}
}

// openFavorites 读 DATA_DIR 下的收藏文件；文件不存在就是空收藏（新部署不预置空文件）。
func openFavorites(dataDir string) (*Favorites, error) {
	favorites := &Favorites{
		path:  filepath.Join(dataDir, favoritesFileName),
		paths: make(map[string]struct{}),
	}
	raw, err := os.ReadFile(favorites.path)
	if errors.Is(err, fs.ErrNotExist) {
		return favorites, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", favorites.path, err)
	}
	var payload favoritesPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse %s: %w", favorites.path, err)
	}
	for _, item := range payload.Paths {
		if item != "" {
			favorites.paths[item] = struct{}{}
		}
	}
	return favorites, nil
}

// All 返回当前收藏的快照（调用方只读，不要修改返回的 map）。
func (f *Favorites) All() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := make(map[string]bool, len(f.paths))
	for item := range f.paths {
		snapshot[item] = true
	}
	return snapshot
}

// Toggle 切换一个路径的收藏状态，返回切换后的状态。
// 路径不校验存在性：收藏记录与只读目录解耦，指向不存在文件的记录读取时忽略。
func (f *Favorites) Toggle(rel string) (bool, error) {
	if err := validateRelPath(rel); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.paths[rel]; ok {
		delete(f.paths, rel)
	} else {
		f.paths[rel] = struct{}{}
	}
	if err := f.saveLocked(); err != nil {
		return false, err
	}
	_, ok := f.paths[rel]
	return ok, nil
}

// saveLocked 原子写 favorites.json：先写临时文件再 rename，避免读到写了一半的文件。
// 调用方必须已持有 f.mu。
func (f *Favorites) saveLocked() error {
	paths := make([]string, 0, len(f.paths))
	for item := range f.paths {
		paths = append(paths, item)
	}
	sort.Strings(paths)
	raw, err := json.Marshal(favoritesPayload{Paths: paths})
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", f.path, err)
	}
	return nil
}
