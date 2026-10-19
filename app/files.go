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

// ListFiles 返回全部文件记录，按创建时间倒序。
func (a *App) ListFiles() ([]db.File, error) {
	files, err := a.Store.ListFiles()
	if err != nil {
		slog.Error("list files failed", slog.Any("error", err))
		return nil, core.Fail(500, "store.list")
	}
	return files, nil
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

// DownloadedFile 是下载需要的记录与已打开的 blob。
type DownloadedFile struct {
	File *db.File
	Blob *os.File
}

// Close 关闭 blob 文件句柄。
func (d *DownloadedFile) Close() error { return d.Blob.Close() }

// OpenDownload 打开一条记录的 blob（需要登录的下载）。
func (a *App) OpenDownload(id string) (*DownloadedFile, error) {
	record, err := a.Store.File(id)
	if err != nil {
		return nil, storeFailure(err, "internal")
	}
	return a.openBlob(record)
}

// OpenPublicDownload 只对公开记录打开 blob；私有与不存在对外表现完全一致。
func (a *App) OpenPublicDownload(id string) (*DownloadedFile, error) {
	record, err := a.Store.PublicFile(id)
	if err != nil {
		return nil, storeFailure(err, "internal")
	}
	return a.openBlob(record)
}

func (a *App) openBlob(record *db.File) (*DownloadedFile, error) {
	blob, err := os.Open(a.blobPath(record.SHA256))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 记录在而 blob 丢了：对外与「记录不存在」一样是 404，不泄露数据目录的状态。
			return nil, core.Fail(404, "file.not-found")
		}
		slog.Error("open blob failed", slog.String("sha256", record.SHA256), slog.Any("error", err))
		return nil, core.Fail(500, "internal")
	}
	return &DownloadedFile{File: record, Blob: blob}, nil
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
	remaining, err := a.Store.CountBySHA(record.SHA256)
	if err != nil {
		// 记录已经删了，此时再报删除失败会误导用户；blob 留下来由运维清理。
		slog.Error("count blob references failed", slog.String("sha256", record.SHA256), slog.Any("error", err))
		return nil
	}
	if remaining > 0 {
		return nil
	}
	if err := os.Remove(a.blobPath(record.SHA256)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// 同上：记录已删，blob 删不掉只是留了个孤儿文件。
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
