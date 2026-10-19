package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// 数据目录与其中文件的权限约定：数据目录只有属主能进，库与 blob 只有属主能读写。
const (
	DataDirPerm = 0o700
	SecretPerm  = 0o600
)

// TightenPerm 把文件或目录的权限改成 mode；路径不存在就跳过
// （启动时的收紧只对已经存在的文件负责，库文件是随后才由 SQLite 建出来的）。
func TightenPerm(path string, mode fs.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

// CheckPerm 校验路径的权限位恰好是 mode：/readyz 用它复查启动时收紧的结果。
func CheckPerm(path string, mode fs.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if got := info.Mode().Perm(); got != mode {
		return fmt.Errorf("%s has permissions %04o, want %04o", path, got, mode)
	}
	return nil
}
