package app

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"file-share/core"
	"file-share/db"
)

// WebDAV 是站点里的第二个命名空间：dav_node 表和 file 表互不可见，
// 但内容共用同一个内容寻址 blob 池（.data/files/<sha256>），引用计数跨表相加。
// 路径都是内部形式（空串是根，其余形如 "a/b/c"），调用方先用 core.NormalizeDavPath 规范化。

// davRootNode 是 WebDAV 的根：它不落库，由应用层合成。
func davRootNode() *db.DavNode {
	return &db.DavNode{Path: "", Parent: "", Name: "/", IsDir: true}
}

// DavStat 取一个节点；根永远存在。
func (a *App) DavStat(path string) (*db.DavNode, error) {
	if path == "" {
		return davRootNode(), nil
	}
	node, err := a.Store.DavNode(path)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, core.Fail(404, "dav.not-found")
		}
		return nil, storeFailure(err, "dav.list")
	}
	return node, nil
}

// DavList 列出一个目录下的直接子节点；路径不是目录按 404 处理。
func (a *App) DavList(path string) ([]db.DavNode, error) {
	if path != "" {
		node, err := a.DavStat(path)
		if err != nil {
			return nil, err
		}
		if !node.IsDir {
			return nil, core.Fail(404, "dav.not-found")
		}
	}
	nodes, err := a.Store.DavChildren(path)
	if err != nil {
		return nil, storeFailure(err, "dav.list")
	}
	return nodes, nil
}

// DavOpenFile 打开一个 WebDAV 文件的 blob；目录与不存在都按 404 处理。
func (a *App) DavOpenFile(path string) (*DownloadedFile, error) {
	node, err := a.DavStat(path)
	if err != nil {
		return nil, err
	}
	if path == "" || node.IsDir {
		return nil, core.Fail(404, "dav.not-found")
	}
	return a.openBlob(node.SHA256, node.Name, node.Size, "dav.not-found")
}

// DavNewFileUpload 为一次 PUT 建临时上传对象：能失败的校验（路径、父目录、目标类型、
// Content-Length）都先做完，再开始收字节。
func (a *App) DavNewFileUpload(path string, contentLength int64) (*Upload, error) {
	if path == "" {
		return nil, core.Fail(400, "dav.invalid")
	}
	if contentLength > a.Config.MaxFileSize {
		return nil, core.Fail(413, "upload.too-big")
	}
	parent, _ := core.SplitPath(path)
	if err := a.davRequireDir(parent); err != nil {
		return nil, err
	}
	if target, err := a.Store.DavNode(path); err == nil {
		if target.IsDir {
			return nil, core.Fail(409, "dav.conflict")
		}
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, storeFailure(err, "dav.write")
	}
	upload, err := a.NewUpload()
	if err != nil {
		slog.Error("create dav upload failed", slog.Any("error", err))
		return nil, core.Fail(500, "dav.write")
	}
	return upload, nil
}

// DavSaveUpload 把一个已经收完的上传落成文件节点：内容按 sha 去重，同名文件整体替换，
// 旧 blob 不再被任何表引用时删掉。
func (a *App) DavSaveUpload(path string, upload *Upload) (*db.DavNode, error) {
	if err := upload.Close(); err != nil {
		upload.Abort()
		slog.Error("dav upload close failed", slog.String("path", path), slog.Any("error", err))
		return nil, core.Fail(500, "dav.write")
	}
	sha, size := upload.Sum(), upload.Size()
	parent, name := core.SplitPath(path)

	a.mu.Lock()
	defer a.mu.Unlock()

	// 收字节期间目标可能被改过，落库前再看一眼。
	if err := a.davRequireDir(parent); err != nil {
		upload.Abort()
		return nil, err
	}
	existing, err := a.Store.DavNode(path)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		upload.Abort()
		return nil, storeFailure(err, "dav.write")
	}
	if existing != nil && existing.IsDir {
		upload.Abort()
		return nil, core.Fail(409, "dav.conflict")
	}

	if err := a.installBlob(upload, sha); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if existing != nil {
		err = a.Store.UpdateDavFile(path, sha, size, now)
	} else {
		err = a.Store.InsertDavNode(db.DavNode{
			Path: path, Parent: parent, Name: name,
			SHA256: sha, Size: size, CreatedAt: now, UpdatedAt: now,
		})
	}
	if err != nil {
		a.pruneBlob(sha)
		return nil, storeFailure(err, "dav.write")
	}
	if existing != nil && existing.SHA256 != sha {
		a.pruneBlob(existing.SHA256)
	}
	node, err := a.Store.DavNode(path)
	if err != nil {
		return nil, storeFailure(err, "dav.write")
	}
	return node, nil
}

// DavPutFile 是一次性写入的便捷入口（请求体直接从内存 / 网络流进来）。
func (a *App) DavPutFile(path string, body io.Reader, contentLength int64) (*db.DavNode, error) {
	upload, err := a.DavNewFileUpload(path, contentLength)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(upload, body); err != nil {
		upload.Abort()
		var business *core.Error
		if errors.As(err, &business) {
			return nil, business
		}
		slog.Error("dav upload copy failed", slog.String("path", path), slog.Any("error", err))
		return nil, core.Fail(500, "dav.write")
	}
	return a.DavSaveUpload(path, upload)
}

// DavMkdir 新建一个目录；父目录不存在或已被占用都按 409 处理。
func (a *App) DavMkdir(path string) error {
	if path == "" {
		return core.Fail(409, "dav.exists")
	}
	parent, name := core.SplitPath(path)
	if err := a.davRequireDir(parent); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().Unix()
	err := a.Store.InsertDavNode(db.DavNode{
		Path: path, Parent: parent, Name: name, IsDir: true, CreatedAt: now, UpdatedAt: now,
	})
	switch {
	case errors.Is(err, db.ErrDavExists):
		return core.Fail(409, "dav.exists")
	case err != nil:
		return storeFailure(err, "dav.mkdir")
	}
	return nil
}

// DavDelete 删除一个节点：目录连同整棵子树一起删，被删文件不再被任何表引用时清 blob。
func (a *App) DavDelete(path string) error {
	if path == "" {
		return core.Fail(403, "dav.invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	shas, err := a.Store.DeleteDavSubtree(path)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return core.Fail(404, "dav.not-found")
		}
		return storeFailure(err, "dav.delete")
	}
	for _, sha := range shas {
		a.pruneBlob(sha)
	}
	return nil
}

// DavMove 改名 / 移动一个节点：目录连同子树一起换前缀。目标存在按 409 处理。
func (a *App) DavMove(oldPath, newPath string) error {
	if oldPath == "" || newPath == "" {
		return core.Fail(403, "dav.invalid")
	}
	if strings.HasPrefix(newPath, oldPath+"/") {
		return core.Fail(400, "dav.invalid")
	}
	parent, _ := core.SplitPath(newPath)
	if err := a.davRequireDir(parent); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch err := a.Store.MoveDavNode(oldPath, newPath); {
	case errors.Is(err, db.ErrNotFound):
		return core.Fail(404, "dav.not-found")
	case errors.Is(err, db.ErrDavExists):
		return core.Fail(409, "dav.exists")
	case err != nil:
		return storeFailure(err, "dav.move")
	}
	return nil
}

// davRequireDir 确认父路径存在且是目录；空路径是根，永远成立。
func (a *App) davRequireDir(path string) error {
	if path == "" {
		return nil
	}
	node, err := a.DavStat(path)
	if err != nil {
		return err
	}
	if !node.IsDir {
		return core.Fail(409, "dav.conflict")
	}
	return nil
}

// installBlob 把上传的临时文件落成 blob。blob 已经存在说明内容重复，临时文件丢掉即可；
// 调用方持有 a.mu，保证「查存在 + 改名」之间不会被别的写操作插进来。
func (a *App) installBlob(upload *Upload, sha string) error {
	_, err := os.Stat(a.blobPath(sha))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Rename(upload.tempPath, a.blobPath(sha)); err != nil {
			upload.Abort()
			slog.Error("move dav upload into place failed", slog.String("sha256", sha), slog.Any("error", err))
			return core.Fail(500, "dav.write")
		}
		upload.consumeTemp()
		return nil
	case err != nil:
		upload.Abort()
		slog.Error("stat blob before dav upload failed", slog.String("sha256", sha), slog.Any("error", err))
		return core.Fail(500, "dav.write")
	default:
		upload.discardTemp()
		return nil
	}
}

// IssueDavDownloadTicket 给一个 WebDAV 文件签短期下载票据：UI 下载时用，
// 避免把 Basic 凭据写进地址栏。
func (a *App) IssueDavDownloadTicket(path string) (string, error) {
	node, err := a.DavStat(path)
	if err != nil {
		return "", err
	}
	if path == "" || node.IsDir {
		return "", core.Fail(404, "dav.not-found")
	}
	if _, err := os.Stat(a.blobPath(node.SHA256)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", core.Fail(404, "dav.not-found")
		}
		slog.Error("stat blob for dav ticket failed", slog.String("sha256", node.SHA256), slog.Any("error", err))
		return "", core.Fail(500, "internal")
	}
	ticket, err := a.tickets.IssueDav(path, time.Now())
	if err != nil {
		slog.Error("issue dav download ticket failed", slog.String("path", path), slog.Any("error", err))
		return "", core.Fail(500, "internal")
	}
	return ticket, nil
}

// VerifyDavDownloadTicket 校验票据并返回 WebDAV 路径；失败一律 404，不区分原因。
func (a *App) VerifyDavDownloadTicket(token string) (string, error) {
	path, err := a.tickets.VerifyDav(token, time.Now())
	if err != nil {
		return "", core.Fail(404, "download.invalid")
	}
	return path, nil
}
