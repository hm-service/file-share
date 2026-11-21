package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"file-share/core"
)

// ErrBlobNotFound 表示 blob 在存储后端里不存在。调用方把它翻译成 404；
// 后端自身的故障（网络、权限、磁盘）原样返回，由上层按 500 处理。
var ErrBlobNotFound = errors.New("blob not found")

// validBlobSHA 校验 sha 是 64 位小写十六进制。业务层本应已经用 core.IsSHA256
// 校验过；这里再挡一次，因为 sha 会直接拼成文件路径 / 对象 key：
// 库里出现异常值时，本地后端不能穿越目录，S3 后端也不能因为切片越界 panic。
func validBlobSHA(sha string) error {
	if !core.IsSHA256(sha) {
		return fmt.Errorf("invalid sha256 %q", sha)
	}
	return nil
}

// BlobStore 是物理文件（blob）的存储后端：按内容 sha256 寻址，一份内容只存一份。
//
// 本地目录与 S3 是两套实现，由 Config.S3 选择；业务层只依赖这个接口，
// 因此上传、秒传、下载、删除的语义在两种后端下保持一致。
type BlobStore interface {
	// Kind 返回后端的简短名字（local / s3），只用于日志。
	Kind() string
	// Head 返回 blob 的字节数；blob 不存在返回 ErrBlobNotFound。
	Head(ctx context.Context, sha string) (size int64, err error)
	// PutFile 把本地文件 path 的内容存成 blob。成功后 path 不再存在
	// （本地实现是 rename，S3 实现是上传后删临时文件）；
	// 失败时由调用方负责清理 path。调用方保证 sha 与文件内容一致。
	PutFile(ctx context.Context, sha, path string, size int64) error
	// Open 打开 blob 供下载。返回的 ReadSeekCloser 支持 Range 读取：
	// http.ServeContent 依赖 Seek 判断大小与定位。blob 不存在返回 ErrBlobNotFound。
	Open(ctx context.Context, sha string) (io.ReadSeekCloser, error)
	// Delete 删除 blob；blob 不存在不算错误。
	Delete(ctx context.Context, sha string) error
}
