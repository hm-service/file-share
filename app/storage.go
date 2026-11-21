package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"file-share/core"
)

// storageCheckTimeout 是 /api/storage 探测后端的上限：探测是给界面看状态用的，
// 不能跟着 S3 的重试策略一起把请求拖成几秒，超时就按「不可用」回报。
const storageCheckTimeout = 2 * time.Second

// CheckStorage 在短超时内探测 blob 后端是否可达。本地后端基本恒成功；
// S3 后端会真实探一次桶，因此能发现「配置在但连不上 / 凭据失效 / 桶被删」。
func (a *App) CheckStorage(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, storageCheckTimeout)
	defer cancel()
	return a.Blobs.Check(ctx)
}

// storageFailure 把 blob 后端的错误翻成对外错误：对象不存在是 404，其余
// （网络、权限、磁盘、桶配置）一律 503。和「服务器内部错误」分开之后，
// 前端才能把「存储暂时不可用，稍后重试」与「服务出了 bug」区分开。
func storageFailure(err error) error {
	if errors.Is(err, ErrBlobNotFound) {
		return core.Fail(http.StatusNotFound, "file.not-found")
	}
	return core.Fail(http.StatusServiceUnavailable, "storage.unavailable")
}
