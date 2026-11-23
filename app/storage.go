package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"file-share/core"
)

// storageCheckTimeout 是 /api/storage 探测的上限：探测是给界面看状态用的，
// 不能把请求拖成几秒，超时就按「不可用」回报。
const storageCheckTimeout = 2 * time.Second

// CheckStorage 在短超时内探测只读数据目录可读、DATA_DIR 可写（收藏元数据）。
// 只读视图没有远程后端，任何读写失败都说明挂载或卷出了问题。
func (a *App) CheckStorage(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, storageCheckTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fs.Stat(a.files.FS(), "."); err != nil {
		return err
	}
	if _, err := os.ReadDir(a.Config.FilesDir); err != nil {
		return err
	}
	probe := filepath.Join(a.Config.DataDir, ".storage-probe")
	if err := os.WriteFile(probe, []byte("ok"), core.SecretPerm); err != nil {
		return err
	}
	defer os.Remove(probe)
	return nil
}
