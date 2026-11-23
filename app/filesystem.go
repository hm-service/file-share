package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// 只读数据目录上的错误：调用方把它们翻译成 404 / 400。
var (
	// ErrNotFound 表示请求的文件在只读目录里不存在（或不是普通文件）。
	ErrNotFound = errors.New("file not found")
	// ErrInvalidPath 表示相对路径不合法：绝对路径、包含 .. / 空段 / 隐藏段。
	ErrInvalidPath = errors.New("invalid path")
)

// File 是只读目录里的一条文件记录，字段与前端 FileItem 对齐。
type File struct {
	// ID 是相对数据根目录的路径（"软件/安卓应用/x.apk"），同时用作下载与分享的标识。
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	GroupPath  string `json:"group_path"` // 父目录相对路径；根目录直属文件为空串
	Size       int64  `json:"size"`
	CreatedAt  int64  `json:"created_at"` // 文件修改时间（unix 秒）
	IsFavorite bool   `json:"is_favorite"`
}

// Group 是只读目录里的一条目录记录。
type Group struct {
	Path   string `json:"path"`
	Parent string `json:"parent"` // 顶级目录为空串
	Name   string `json:"name"`
}

// treeSnapshot 是一次扫描得到的目录与文件。
type treeSnapshot struct {
	files  []File
	groups []Group
}

// scan 递归扫描数据目录并按用户的可见范围过滤：目录即分组，普通文件即文件记录。
//
// 规则：
//   - 点开头的文件 / 目录一律跳过（隐藏文件不对外，`.file-share` 之类的元数据目录也不展示）；
//   - 符号链接与设备等非常规文件跳过（只读视图不跟随链接，避免逃出数据目录）；
//   - 目录深度不限制（SMB 那边怎么组织就怎么展示）；
//   - 排序：分组按（父路径、名称）排序，文件按（分组路径、名称）排序，名称比较忽略大小写。
func (a *App) scan(user User) (treeSnapshot, error) {
	var snap treeSnapshot
	err := fs.WalkDir(a.files.FS(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), "./")
		// 不可见路径整棵跳过：不可访问的目录与文件从不进入列表，用户视角里不存在。
		// 祖先链由 User.Visible 保留，用户仍能看到自己处在哪一层。
		if !user.Visible(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		if d.IsDir() {
			snap.groups = append(snap.groups, Group{Path: rel, Parent: parent, Name: name})
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %q: %w", rel, err)
		}
		snap.files = append(snap.files, File{
			ID:        rel,
			Filename:  name,
			GroupPath: parent,
			Size:      info.Size(),
			CreatedAt: info.ModTime().Unix(),
		})
		return nil
	})
	if err != nil {
		return treeSnapshot{}, err
	}
	sort.Slice(snap.groups, func(i, j int) bool {
		if snap.groups[i].Parent != snap.groups[j].Parent {
			return snap.groups[i].Parent < snap.groups[j].Parent
		}
		return lessName(snap.groups[i].Name, snap.groups[j].Name)
	})
	sort.Slice(snap.files, func(i, j int) bool {
		if snap.files[i].GroupPath != snap.files[j].GroupPath {
			return snap.files[i].GroupPath < snap.files[j].GroupPath
		}
		return lessName(snap.files[i].Filename, snap.files[j].Filename)
	})
	return snap, nil
}

// lessName 按大小写不敏感比较名称，忽略大小写后仍相同就退回逐字节比较，保证顺序稳定。
func lessName(a, b string) bool {
	lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
	if lowerA != lowerB {
		return lowerA < lowerB
	}
	return a < b
}

// validateRelPath 校验外部传入的相对路径：拒绝绝对路径、反斜杠、空段与任何点开头的段。
// 允许子目录（"a/b/c"），但每一段都必须是非空的普通名字。
func validateRelPath(rel string) error {
	if rel == "" || rel == "." {
		return ErrInvalidPath
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return ErrInvalidPath
	}
	if strings.ContainsRune(rel, '\\') {
		return ErrInvalidPath
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalidPath
		}
		if strings.HasPrefix(segment, ".") {
			return ErrInvalidPath
		}
	}
	return nil
}

// openFile 在数据目录根下打开一个相对路径的只读文件句柄。
//
// 路径上每一段都先 Lstat：os.Root 只阻止逃出数据目录，但仍会跟随数据目录内部的符号链接。
// 若 /目录A/链接 指向 /目录B，只做前缀校验就放行会绕过可见范围，所以段级符号链接一律拒绝。
func openFile(root *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	if err := validateRelPath(rel); err != nil {
		return nil, nil, err
	}
	segments := strings.Split(rel, "/")
	prefix := ""
	for index, segment := range segments {
		prefix = path.Join(prefix, segment)
		info, err := root.Lstat(filepath.FromSlash(prefix))
		if err != nil {
			return nil, nil, translateFileError(err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, nil, ErrNotFound
		}
		if index < len(segments)-1 && !info.IsDir() {
			return nil, nil, ErrNotFound
		}
	}
	name := filepath.FromSlash(rel)
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, translateFileError(err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, ErrNotFound
	}
	return file, info, nil
}

// translateFileError 把底层文件错误收缩成对外两类：不存在 / 其余原样返回。
func translateFileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// checkAccessPath 校验一条可见范围：路径上每一段都必须是真实目录，不能是符号链接。
// 可见范围是访问边界，配错就该拒绝启动；段级符号链接会让「前缀匹配」指向别处，同样拒绝。
func checkAccessPath(files *os.Root, access string) error {
	rel := strings.TrimPrefix(access, "/")
	if rel == "" {
		return nil
	}
	prefix := ""
	for _, segment := range strings.Split(rel, "/") {
		prefix = path.Join(prefix, segment)
		info, err := files.Lstat(filepath.FromSlash(prefix))
		if err != nil {
			return fmt.Errorf("access %q: %w", access, translateFileError(err))
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("access %q: %q is a symbolic link", access, prefix)
		}
		if !info.IsDir() {
			return fmt.Errorf("access %q: %q is not a directory", access, prefix)
		}
	}
	return nil
}
