package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"file-share/core"
	"file-share/db"
)

// Upload 是一次上传的临时落点：内容边写边算 sha256，校验通过后才变成按 sha 命名的 blob。
//
// 单独抽出这个对象是因为 multipart 的字段顺序不固定：遇到 file 字段时必须立刻流式落盘，
// 而 client_sha256 可能在其后到达，只能等整段请求读完再决定这份临时文件留不留。
type Upload struct {
	file     *os.File
	tempPath string
	hash     hash.Hash
	size     int64
	max      int64

	closed      bool
	done        bool
	writeFailed bool
}

// NewUpload 在 files/ 下建一个私密权限的临时文件。
func (a *App) NewUpload() (*Upload, error) {
	id, err := core.UUID4()
	if err != nil {
		return nil, err
	}
	tempPath := filepath.Join(a.filesDir, ".upload-"+id+".tmp")
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, core.SecretPerm)
	if err != nil {
		return nil, err
	}
	return &Upload{file: file, tempPath: tempPath, hash: sha256.New(), max: a.Config.MaxFileSize}, nil
}

// Write 把上传内容写进临时文件并同步更新摘要；超过 MAX_FILE_SIZE 立即拒绝。
func (u *Upload) Write(p []byte) (int, error) {
	if u.done {
		return 0, os.ErrClosed
	}
	if u.size+int64(len(p)) > u.max {
		return 0, core.Fail(413, "upload.too-big")
	}
	n, err := u.file.Write(p)
	if err != nil {
		u.writeFailed = true
		return n, err
	}
	u.hash.Write(p[:n])
	u.size += int64(n)
	return n, nil
}

// Size 返回已经写入的字节数。
func (u *Upload) Size() int64 { return u.size }

// Sum 返回已写内容的 sha256（小写十六进制）。
func (u *Upload) Sum() string { return hex.EncodeToString(u.hash.Sum(nil)) }

// WriteFailed 报告上一次错误是不是来自本地磁盘写入（而不是请求体读取）。
func (u *Upload) WriteFailed() bool { return u.writeFailed }

// Close 关闭临时文件；落盘前必须先关闭，确保内容完整刷到磁盘。
func (u *Upload) Close() error {
	if u.closed {
		return nil
	}
	u.closed = true
	return u.file.Close()
}

// Abort 关闭并删除临时文件；调用方在错误路径上 defer 它，重复调用是安全的。
func (u *Upload) Abort() {
	if u == nil || u.done {
		return
	}
	u.done = true
	_ = u.Close()
	_ = os.Remove(u.tempPath)
}

// discardTemp 丢掉临时文件（blob 已存在或落盘失败时用）。
func (u *Upload) discardTemp() {
	u.Abort()
}

// consumeTemp 标记临时文件已经重命名成 blob，不该再被删掉。
func (u *Upload) consumeTemp() { u.done = true }

// FinishUpload 校验上传结果、把临时文件落成 blob，并插入一条文件记录。
// 任何错误路径都会清掉临时文件。
func (a *App) FinishUpload(upload *Upload, filename, clientSHA string) (*db.File, error) {
	fail := func(err error) (*db.File, error) {
		upload.Abort()
		return nil, err
	}
	if clientSHA == "" {
		return fail(core.Fail(400, "upload.missing"))
	}
	if filename == "" {
		return fail(core.Fail(400, "upload.missing"))
	}
	if !core.IsSHA256(clientSHA) {
		return fail(core.Fail(400, "upload.invalid-sha256"))
	}
	if !core.ValidFilename(filename) {
		return fail(core.Fail(400, "upload.invalid-filename"))
	}
	if upload.Sum() != clientSHA {
		return fail(core.Fail(400, "upload.checksum-mismatch"))
	}
	if err := upload.Close(); err != nil {
		slog.Error("close upload temp file failed", slog.Any("error", err))
		return fail(core.Fail(500, "upload.write"))
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	blobPath := a.blobPath(clientSHA)
	switch _, err := os.Stat(blobPath); {
	case err == nil:
		// 并发秒传：blob 已经在（内容必然与 sha 一致），临时文件没有用了。
		upload.discardTemp()
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Rename(upload.tempPath, blobPath); err != nil {
			slog.Error("move upload into place failed", slog.Any("error", err))
			upload.discardTemp()
			return nil, core.Fail(500, "upload.write")
		}
		upload.consumeTemp()
	default:
		slog.Error("stat blob before moving upload failed", slog.Any("error", err))
		upload.discardTemp()
		return nil, core.Fail(500, "upload.write")
	}

	record, err := newFileRecord(filename, clientSHA, upload.Size())
	if err != nil {
		slog.Error("generate file id failed", slog.Any("error", err))
		return nil, core.Fail(500, "internal")
	}
	if err := a.Store.InsertFile(*record); err != nil {
		slog.Error("insert file record failed", slog.Any("error", err))
		return nil, core.Fail(500, "store.insert")
	}
	return record, nil
}
