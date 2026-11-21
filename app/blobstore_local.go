package app

import (
	"context"
	"errors"
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

func (s *localBlobStore) path(sha string) string { return filepath.Join(s.dir, sha) }

func (s *localBlobStore) Head(_ context.Context, sha string) (int64, error) {
	if err := validBlobSHA(sha); err != nil {
		return 0, err
	}
	info, err := os.Stat(s.path(sha))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
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
		if errors.Is(err, fs.ErrNotExist) {
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
