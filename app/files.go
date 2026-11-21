package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

// CheckFile 是秒传预检：blob 已在存储后端就立刻补一条记录（新的记录默认不是收藏）。
// 返回的记录只在 exists 为 true 时有效。
func (a *App) CheckFile(ctx context.Context, clientSHA, filename string) (file *db.File, exists bool, err error) {
	if clientSHA == "" || filename == "" {
		return nil, false, core.Fail(400, "upload.missing")
	}
	if !core.IsSHA256(clientSHA) {
		return nil, false, core.Fail(400, "upload.invalid-sha256")
	}
	if !core.ValidFilename(filename) {
		return nil, false, core.Fail(400, "upload.invalid-filename")
	}

	// 与 FinishUpload 走同一把 sha 分片锁：Head 之后、插记录之前不会有删除动作
	// 把这个 blob 清掉，避免出现「记录在、blob 不在」。
	lock := a.shaMutex(clientSHA)
	lock.Lock()
	defer lock.Unlock()

	size, err := a.Blobs.Head(ctx, clientSHA)
	if errors.Is(err, ErrBlobNotFound) {
		return nil, false, nil
	}
	if err != nil {
		slog.Error("stat blob for instant upload failed", slog.Any("error", err))
		return nil, false, storageFailure(err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	record, err := newFileRecord(filename, clientSHA, size)
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

// RenameGroup 改写一个分组路径的最后一段，同级位置不变；它下面的全部后代分组与组内文件一起改名。
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

// DeleteGroup 删除一个分组，它的全部后代分组一并删除；deleteFiles 决定组内文件是一起删掉
// 还是上移到被删分组的父路径（顶级分组上移到未分组）。目标分组不存在按 404 返回。
func (a *App) DeleteGroup(ctx context.Context, rawPath string, deleteFiles bool) error {
	path, ok := core.NormalizeGroupPath(rawPath)
	if !ok || path == "" {
		return core.Fail(400, "group.invalid")
	}
	a.mu.Lock()
	shas, err := a.Store.DeleteGroup(path, deleteFiles)
	a.mu.Unlock()
	switch {
	case errors.Is(err, db.ErrNotFound):
		return core.Fail(404, "group.not-found")
	case err != nil:
		return storeFailure(err, "store.delete")
	}
	// 记录已经删掉：blob 只剩这个分组引用时才清理，删不掉只留孤儿对象、不影响响应。
	// 逐个 sha 拿分片锁，避免和并发上传/删除互相踩；锁顺序固定为 sha → 无（DB 自己串行）。
	for _, sha := range shas {
		a.removeBlobIfUnreferenced(ctx, sha)
	}
	return nil
}

// removeBlobIfUnreferenced 在分片锁内确认这个 sha 已无记录引用再删 blob。
// 用 defer 解锁而不是手动 unlock：S3 模式下 Blobs.Delete 可能 panic，
// 手动解锁会把这把分片锁永远留在锁定状态。
func (a *App) removeBlobIfUnreferenced(ctx context.Context, sha string) {
	lock := a.shaMutex(sha)
	lock.Lock()
	defer lock.Unlock()

	remaining, err := a.Store.CountBySHA(sha)
	if err != nil {
		slog.Error("count blob references after group delete failed",
			slog.String("sha256", sha), slog.Any("error", err))
		return
	}
	if remaining > 0 {
		return
	}
	if err := a.Blobs.Delete(ctx, sha); err != nil {
		slog.Error("remove blob after group delete failed",
			slog.String("sha256", sha), slog.Any("error", err))
	}
}

// IssueDownloadURL 给一条记录签匿名下载地址：浏览器凭它直接导航下载，
// 地址 SignedURLTTL 内有效。blob 已经丢了就当场按 404 拒绝，
// 避免用户导航过去才看到一个错误页。
func (a *App) IssueDownloadURL(ctx context.Context, id string) (string, error) {
	return a.issueSignedURL(ctx, id, core.SignedURLExpiry(time.Now(), core.SignedURLTTL))
}

// IssueShareLink 给一条记录签分享链接：登录后复制，对方 ttl 内免登录下载。
// ttl 由调用方限定（只允许默认档与 24 小时档），这里不猜。
func (a *App) IssueShareLink(ctx context.Context, id string, ttl time.Duration) (string, error) {
	return a.issueSignedURL(ctx, id, core.SignedURLExpiry(time.Now(), ttl))
}

// issueSignedURL 是下载地址与分享链接共用的签发流程：记录与 blob 都在才签，
// 签的路径固定为 /s/<id>，签名因此绑定单个文件。
func (a *App) issueSignedURL(ctx context.Context, id string, exp int64) (string, error) {
	record, err := a.Store.File(id)
	if err != nil {
		return "", storeFailure(err, "internal")
	}
	if _, err := a.Blobs.Head(ctx, record.SHA256); err != nil {
		if !errors.Is(err, ErrBlobNotFound) {
			slog.Error("stat blob for signed url failed", slog.Any("error", err))
		}
		return "", storageFailure(err)
	}
	return a.signurl.SignedURL("/s/"+record.ID, exp), nil
}

// OpenSignedDownload 校验签名 URL 并打开 blob。校验失败一律 404：
// 匿名调用方不需要知道是伪造、过期还是字段无效。escapedPath 传请求的 EscapedPath，
// 与签出时逐字节一致才算数。
func (a *App) OpenSignedDownload(ctx context.Context, fileID, method, escapedPath, sig string, exp int64) (*DownloadedFile, error) {
	if err := a.signurl.Verify(method, escapedPath, sig, exp, time.Now()); err != nil {
		return nil, core.Fail(404, "download.invalid")
	}
	return a.OpenDownload(ctx, fileID)
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

// DownloadedFile 是下载需要的记录与已打开的 blob。
// Blob 只要求能 Seek 与读：本地实现是 *os.File，S3 实现是能按 Range 重开的对象流，
// 这样 api 层可以继续用 http.ServeContent 拿到完整的 Range / HEAD 语义。
type DownloadedFile struct {
	File *db.File
	Blob io.ReadSeekCloser
}

// Close 关闭 blob 句柄。
func (d *DownloadedFile) Close() error { return d.Blob.Close() }

// OpenDownload 打开一条记录的 blob（需要登录的下载，以及凭签名 URL 的匿名下载）。
func (a *App) OpenDownload(ctx context.Context, id string) (*DownloadedFile, error) {
	record, err := a.Store.File(id)
	if err != nil {
		return nil, storeFailure(err, "internal")
	}
	return a.openBlob(ctx, record)
}

func (a *App) openBlob(ctx context.Context, record *db.File) (*DownloadedFile, error) {
	blob, err := a.Blobs.Open(ctx, record.SHA256)
	if err != nil {
		if !errors.Is(err, ErrBlobNotFound) {
			slog.Error("open blob failed", slog.String("sha256", record.SHA256), slog.Any("error", err))
		}
		// 记录在而 blob 丢了：对外与「记录不存在」一样是 404，不泄露存储后端的状态。
		return nil, storageFailure(err)
	}
	return &DownloadedFile{File: record, Blob: blob}, nil
}

// DeleteFile 删除一条记录；这个 sha256 已无其他记录引用时顺手删掉 blob。
func (a *App) DeleteFile(ctx context.Context, id string) error {
	record, err := a.Store.File(id)
	if err != nil {
		return storeFailure(err, "store.delete")
	}

	// sha 分片锁覆盖「删记录 + 数引用 + 删 blob」，防止并发上传在两步之间插入。
	lock := a.shaMutex(record.SHA256)
	lock.Lock()
	defer lock.Unlock()

	a.mu.Lock()
	err = a.Store.DeleteFile(id)
	if err != nil {
		a.mu.Unlock()
		return storeFailure(err, "store.delete")
	}
	remaining, countErr := a.Store.CountBySHA(record.SHA256)
	a.mu.Unlock()

	if countErr != nil {
		// 记录已经删了，此时再报删除失败会误导用户；blob 留下来由运维清理。
		slog.Error("count blob references failed", slog.String("sha256", record.SHA256), slog.Any("error", countErr))
		return nil
	}
	if remaining > 0 {
		return nil
	}
	if err := a.Blobs.Delete(ctx, record.SHA256); err != nil {
		// 同上：记录已删，blob 删不掉只是留了个孤儿对象。
		slog.Error("remove unreferenced blob failed", slog.String("sha256", record.SHA256), slog.Any("error", err))
	}
	return nil
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
