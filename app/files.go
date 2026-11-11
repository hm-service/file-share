package app

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"file-share/core"
	"file-share/db"
)

// ListFiles 返回全部文件记录，按用户排定的顺序（sort_order）与创建时间排序。
func (a *App) ListFiles() ([]db.File, error) {
	files, err := a.Store.ListFiles()
	if err != nil {
		slog.Error("list files failed", slog.Any("error", err))
		return nil, core.Fail(500, "store.list")
	}
	return files, nil
}

// ListGroups 返回全部分组（含空分组）；顺序先按父路径、再按同级 sort_order。
func (a *App) ListGroups() ([]db.Group, error) {
	groups, err := a.Store.Groups()
	if err != nil {
		slog.Error("list groups failed", slog.Any("error", err))
		return nil, core.Fail(500, "store.groups")
	}
	return groups, nil
}

// CreateGroup 手动新建一个空分组，返回规范化后的路径；父级不存在时一并创建。
// 路径已存在返回 409。未分组不是一个分组，空路径一律拒绝。
func (a *App) CreateGroup(rawPath string) (string, error) {
	path, ok := core.NormalizeGroupPath(rawPath)
	if !ok || path == "" {
		return "", core.Fail(400, "group.invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.Store.CreateGroup(path)
	switch {
	case errors.Is(err, db.ErrGroupExists):
		return "", core.Fail(409, "group.exists")
	case err != nil:
		return "", storeFailure(err, "store.create")
	}
	return path, nil
}

// SetGroupOrder 按拖拽后的顺序重写某个父级下子分组的顺序；
// 提交的分组名必须与当前该父级下的集合完全一致，父级用空串表示顶级。
func (a *App) SetGroupOrder(rawParent string, names []string) error {
	parent, ok := core.NormalizeGroupPath(rawParent)
	if !ok {
		return core.Fail(400, "order.invalid")
	}
	if len(names) > core.MaxOrderItems {
		return core.Fail(400, "order.invalid")
	}
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name, ok := core.NormalizeGroupSegment(raw)
		if !ok || name != raw {
			return core.Fail(400, "order.invalid")
		}
		if _, dup := seen[name]; dup {
			return core.Fail(400, "order.invalid")
		}
		seen[name] = struct{}{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.Store.SetGroupOrder(parent, names)
	if errors.Is(err, db.ErrGroupOrderMismatch) {
		return core.Fail(409, "group.changed")
	}
	return storeFailure(err, "store.order")
}

// CheckFile 是秒传预检：blob 已在磁盘上就立刻补一条记录（新的记录默认既非收藏也非公开）。
// 返回的记录只在 exists 为 true 时有效。
func (a *App) CheckFile(clientSHA, filename string) (file *db.File, exists bool, err error) {
	if clientSHA == "" || filename == "" {
		return nil, false, core.Fail(400, "upload.missing")
	}
	if !core.IsSHA256(clientSHA) {
		return nil, false, core.Fail(400, "upload.invalid-sha256")
	}
	if !core.ValidFilename(filename) {
		return nil, false, core.Fail(400, "upload.invalid-filename")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	info, statErr := os.Stat(a.blobPath(clientSHA))
	if errors.Is(statErr, fs.ErrNotExist) {
		return nil, false, nil
	}
	if statErr != nil {
		slog.Error("stat blob for instant upload failed", slog.Any("error", statErr))
		return nil, false, core.Fail(500, "internal")
	}
	record, err := newFileRecord(filename, clientSHA, info.Size())
	if err != nil {
		slog.Error("generate file id failed", slog.Any("error", err))
		return nil, false, core.Fail(500, "internal")
	}
	if err := a.Store.InsertFile(*record); err != nil {
		slog.Error("insert instant upload record failed", slog.Any("error", err))
		return nil, false, core.Fail(500, "store.insert")
	}
	return record, true, nil
}

// UpdateFile 按需改写文件名（filename）与分组路径（groupPath），指针为 nil 表示不动这个字段。
// 改分组会把记录移到列表末尾；只改名保持原顺序。
func (a *App) UpdateFile(id string, filename, rawGroupPath *string) (*db.File, error) {
	if filename == nil && rawGroupPath == nil {
		return nil, core.Fail(400, "request.empty")
	}
	if filename != nil && !core.ValidFilename(*filename) {
		return nil, core.Fail(400, "upload.invalid-filename")
	}
	var groupPath *string
	if rawGroupPath != nil {
		normalized, ok := core.NormalizeGroupPath(*rawGroupPath)
		if !ok {
			return nil, core.Fail(400, "group.invalid")
		}
		groupPath = &normalized
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	record, err := a.Store.UpdateFile(id, filename, groupPath)
	if err != nil {
		return nil, storeFailure(err, "internal")
	}
	return record, nil
}

// RenameGroup 改写一个分组路径的最后一段，同级位置不变；顶级分组的子分组与组内文件一起改名。
// 目标分组已存在时拒绝（不做隐式合并）；源分组不存在按 404 返回。
func (a *App) RenameGroup(rawPath, rawNewName string) error {
	path, ok := core.NormalizeGroupPath(rawPath)
	if !ok || path == "" {
		return core.Fail(400, "group.invalid")
	}
	newName, ok := core.NormalizeGroupSegment(rawNewName)
	if !ok {
		return core.Fail(400, "group.invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.Store.RenameGroup(path, newName)
	switch {
	case errors.Is(err, db.ErrGroupExists):
		return core.Fail(409, "group.exists")
	case errors.Is(err, db.ErrNotFound):
		return core.Fail(404, "group.not-found")
	}
	return storeFailure(err, "store.rename")
}

// DeleteGroup 删除一个分组，子分组一并删除；deleteFiles 决定组内文件是一起删掉还是上移
// 到被删分组的父路径（顶级分组上移到未分组）。目标分组不存在按 404 返回。
func (a *App) DeleteGroup(rawPath string, deleteFiles bool) error {
	path, ok := core.NormalizeGroupPath(rawPath)
	if !ok || path == "" {
		return core.Fail(400, "group.invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	shas, err := a.Store.DeleteGroup(path, deleteFiles)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return core.Fail(404, "group.not-found")
	case err != nil:
		return storeFailure(err, "store.delete")
	}
	// 记录已经删掉：blob 只剩这个分组引用时才清理，删不掉只留孤儿文件、不影响响应。
	for _, sha := range shas {
		a.pruneBlob(sha)
	}
	return nil
}

// IssueFileDownloadTicket 给一条记录签一张短期下载票据，浏览器凭它直接导航下载。
// blob 已经丢了就当场按 404 拒绝，避免用户导航过去才看到一个错误页。
func (a *App) IssueFileDownloadTicket(id string) (string, error) {
	record, err := a.Store.File(id)
	if err != nil {
		return "", storeFailure(err, "internal")
	}
	if _, err := os.Stat(a.blobPath(record.SHA256)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", core.Fail(404, "file.not-found")
		}
		slog.Error("stat blob for download ticket failed", slog.Any("error", err))
		return "", core.Fail(500, "internal")
	}
	ticket, err := a.tickets.Issue(record.ID, time.Now())
	if err != nil {
		slog.Error("issue file download ticket failed", slog.Any("error", err))
		return "", core.Fail(500, "internal")
	}
	return ticket, nil
}

// VerifyDownloadTicket 校验下载票据并返回文件 id。过期 / 签名不对 / 字段无效统一回一枚 404：
// 下载路由不需要告诉匿名调用方「是过期了还是伪造的」。
func (a *App) VerifyDownloadTicket(token string) (string, error) {
	fileID, err := a.tickets.Verify(token, time.Now())
	if err != nil {
		return "", core.Fail(404, "download.invalid")
	}
	return fileID, nil
}

// SetOrder 批量重写列表顺序；任一条记录不存在就整体失败，原顺序保持不变。
func (a *App) SetOrder(items []core.OrderItem) error {
	if !core.ValidOrderItems(items) {
		return core.Fail(400, "order.invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return storeFailure(a.Store.SetOrder(items), "internal")
}

// ToggleFavorite 翻转收藏并返回翻转后的状态。
func (a *App) ToggleFavorite(id string) (bool, error) {
	favorite, err := a.Store.ToggleFavorite(id)
	return favorite, storeFailure(err, "store.favorite")
}

// TogglePublic 翻转公开状态并返回翻转后的状态。
func (a *App) TogglePublic(id string) (bool, error) {
	isPublic, err := a.Store.TogglePublic(id)
	return isPublic, storeFailure(err, "store.public")
}

// storeFailure 把 db 层错误翻译成 HTTP 层认识的业务错误：记录不存在是 404，其余是 500。
func storeFailure(err error, key string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, db.ErrNotFound) {
		return core.Fail(404, "file.not-found")
	}
	slog.Error("file store operation failed", slog.String("key", key), slog.Any("error", err))
	return core.Fail(500, key)
}

// DownloadedFile 是下载需要的元信息与已打开的 blob。它不绑定某张表：
// file 表与 dav_node 表都通过它把内容交给 HTTP 层。
type DownloadedFile struct {
	// Name 是下载时写进 Content-Disposition 的文件名（取自记录或 WebDAV 路径最后一段）。
	Name   string
	SHA256 string
	Size   int64
	Blob   *os.File
}

// Close 关闭 blob 文件句柄。
func (d *DownloadedFile) Close() error { return d.Blob.Close() }

// OpenDownload 打开一条记录的 blob（需要登录的下载）。
func (a *App) OpenDownload(id string) (*DownloadedFile, error) {
	record, err := a.Store.File(id)
	if err != nil {
		return nil, storeFailure(err, "internal")
	}
	return a.openBlob(record.SHA256, record.Filename, record.Size, "file.not-found")
}

// OpenPublicDownload 只对公开记录打开 blob；私有与不存在对外表现完全一致。
func (a *App) OpenPublicDownload(id string) (*DownloadedFile, error) {
	record, err := a.Store.PublicFile(id)
	if err != nil {
		return nil, storeFailure(err, "internal")
	}
	return a.openBlob(record.SHA256, record.Filename, record.Size, "file.not-found")
}

// openBlob 按 sha256 打开物理文件；记录在而 blob 丢了按 404 处理，不泄露数据目录的状态。
func (a *App) openBlob(sha, name string, size int64, notFoundKey string) (*DownloadedFile, error) {
	blob, err := os.Open(a.blobPath(sha))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, core.Fail(404, notFoundKey)
		}
		slog.Error("open blob failed", slog.String("sha256", sha), slog.Any("error", err))
		return nil, core.Fail(500, "internal")
	}
	return &DownloadedFile{Name: name, SHA256: sha, Size: size, Blob: blob}, nil
}

// DeleteFile 删除一条记录；这个 sha256 已无其他记录引用时顺手删掉 blob。
func (a *App) DeleteFile(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	record, err := a.Store.File(id)
	if err != nil {
		return storeFailure(err, "store.delete")
	}
	if err := a.Store.DeleteFile(id); err != nil {
		return storeFailure(err, "store.delete")
	}
	a.pruneBlob(record.SHA256)
	return nil
}

// pruneBlob 在记录删除之后调用：file 与 dav_node 都不再引用这个 sha 时才删物理文件。
// 记录已经删了，这里的失败只留下孤儿文件，不把错误回给调用方。
func (a *App) pruneBlob(sha string) {
	remaining, err := a.Store.CountBySHA(sha)
	if err != nil {
		slog.Error("count blob references failed", slog.String("sha256", sha), slog.Any("error", err))
		return
	}
	if remaining > 0 {
		return
	}
	if err := os.Remove(a.blobPath(sha)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("remove unreferenced blob failed", slog.String("sha256", sha), slog.Any("error", err))
	}
}

func newFileRecord(filename, sha string, size int64) (*db.File, error) {
	id, err := core.UUID4()
	if err != nil {
		return nil, err
	}
	return &db.File{
		ID:        id,
		Filename:  filename,
		SHA256:    sha,
		Size:      size,
		CreatedAt: time.Now().Unix(),
	}, nil
}
