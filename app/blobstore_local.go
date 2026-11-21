package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// localBlobStore 把 blob 存成 DATA_DIR/files/<sha256>：沿用改造前的布局，
// 文件名就是内容摘要，去重靠同名的文件是否存在。
type localBlobStore struct {
	dir string
}

func newLocalBlobStore(dir string) BlobStore { return &localBlobStore{dir: dir} }

func (s *localBlobStore) Kind() string { return "local" }

// Check 确认 blob 目录还在：本地后端没有远端依赖，目录被移走 / 卷未挂载才算故障。
func (s *localBlobStore) Check(_ context.Context) error {
	info, err := os.Stat(s.dir)
	if err != nil {
		return fmt.Errorf("blob dir %s: %w", s.dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("blob dir %s is not a directory", s.dir)
	}
	return nil
}

func (s *localBlobStore) path(sha string) string { return filepath.Join(s.dir, sha) }

// blobMissing 判断 stat/open 的错误是不是「blob 文件不存在」。blob 目录本身不见了
// （卷未挂载、目录被移走）属于存储不可用，不能冒充成 404 —— 否则界面会报「文件不存在」。
func (s *localBlobStore) blobMissing(err error) bool {
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, dirErr := os.Stat(s.dir)
	return dirErr == nil
}

func (s *localBlobStore) Head(_ context.Context, sha string) (int64, error) {
	if err := validBlobSHA(sha); err != nil {
		return 0, err
	}
	info, err := os.Stat(s.path(sha))
	if err != nil {
		if s.blobMissing(err) {
			return 0, ErrBlobNotFound
		}
		return 0, err
	}
	return info.Size(), nil
}

func (s *localBlobStore) PutFile(_ context.Context, sha, path string, _ int64) error {
	if err := validBlobSHA(sha); err != nil {
		return err
	}
	// 同一目录内 rename：原子且不复制内容。sha 相同的内容必然一致，
	// 目标已存在时覆盖的也是同一份内容，不需要先判存在。
	return os.Rename(path, s.path(sha))
}

func (s *localBlobStore) Open(_ context.Context, sha string) (io.ReadSeekCloser, error) {
	if err := validBlobSHA(sha); err != nil {
		return nil, err
	}
	file, err := os.Open(s.path(sha))
	if err != nil {
		if s.blobMissing(err) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	return file, nil
}

func (s *localBlobStore) Delete(_ context.Context, sha string) error {
	if err := validBlobSHA(sha); err != nil {
		return err
	}
	if err := os.Remove(s.path(sha)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
