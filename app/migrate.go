package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"file-share/db"
)

// MigrateToS3 把本地 DATA_DIR/blobs/<sha256> 里的 blob 搬到 S3。
//
// 幂等：目标对象已存在且大小一致就跳过，可反复执行。迁移只上传与校验，
// 不删除本地文件 —— 回滚依赖它们还在，确认无误后由运维自行清理。
// 迁移期间应用应停止写入（停服或用只读窗口），否则可能漏掉迁移后才上传的 blob。
func MigrateToS3(ctx context.Context, dataDir string, cfg S3Config, dryRun bool, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if err := moveLegacyBlobDir(dataDir); err != nil {
		return err
	}
	dbPath := filepath.Join(dataDir, dbFileName)
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database %s is not readable: %w", dbPath, err)
	}
	localDir := filepath.Join(dataDir, blobsDirName)
	if info, err := os.Stat(localDir); err != nil || !info.IsDir() {
		return fmt.Errorf("local blob dir %s is not readable: %w", localDir, err)
	}
	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	remote, err := newS3BlobStore(cfg)
	if err != nil {
		return err
	}

	files, err := store.ListFiles()
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}
	// 同一个 sha 可能被多条记录引用，只搬一次；大小取记录里的值，本地文件再校验一遍。
	blobs := make(map[string]int64, len(files))
	for _, file := range files {
		blobs[file.SHA256] = file.Size
	}
	fmt.Fprintf(out, "database: %d files, %d distinct blobs\n", len(files), len(blobs))
	if dryRun {
		fmt.Fprintln(out, "dry run: nothing will be uploaded")
	}

	uploadVerb := "uploaded"
	if dryRun {
		uploadVerb = "would upload"
	}
	var uploaded, skipped, failed int
	for sha, size := range blobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 先看远端：已经搬过的直接跳过，重复执行不必再读一遍本地 30GB+ 的文件。
		remoteSize, remoteErr := remote.Head(ctx, sha)
		switch {
		case remoteErr == nil:
			if remoteSize != size {
				fmt.Fprintf(out, "FAIL %s: remote object has size %d, want %d\n", sha, remoteSize, size)
				failed++
				continue
			}
			skipped++
			continue
		case !errors.Is(remoteErr, ErrBlobNotFound):
			fmt.Fprintf(out, "FAIL %s: check remote: %v\n", sha, remoteErr)
			failed++
			continue
		}

		path := filepath.Join(localDir, sha)
		if err := verifyLocalBlob(path, sha, size); err != nil {
			fmt.Fprintf(out, "FAIL %s: local blob: %v\n", sha, err)
			failed++
			continue
		}
		if dryRun {
			fmt.Fprintf(out, "would upload %s (%d bytes)\n", sha, size)
			uploaded++
			continue
		}
		if err := remote.putPath(ctx, sha, path, size); err != nil {
			fmt.Fprintf(out, "FAIL %s: upload: %v\n", sha, err)
			failed++
			continue
		}
		// 上传后再 Head 一次：确认对象真的在、大小对得上。
		got, err := remote.Head(ctx, sha)
		if err != nil || got != size {
			fmt.Fprintf(out, "FAIL %s: verify uploaded object: size=%d err=%v\n", sha, got, err)
			failed++
			continue
		}
		uploaded++
		fmt.Fprintf(out, "uploaded %s (%d bytes)\n", sha, size)
	}

	fmt.Fprintf(out, "\nsummary: %s %d, skipped %d, failed %d\n", uploadVerb, uploaded, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d blobs failed", failed)
	}
	return nil
}

// verifyLocalBlob 确认本地文件存在、大小与记录一致、内容摘要等于文件名。
func verifyLocalBlob(path, sha string, wantSize int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != wantSize {
		return fmt.Errorf("size %d does not match record size %d", info.Size(), wantSize)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != sha {
		return fmt.Errorf("sha256 %s does not match name %s", got, sha)
	}
	return nil
}
