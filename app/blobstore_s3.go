package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config 是 S3 后端的连接参数；Config.S3 为 nil 时用本地目录。
type S3Config struct {
	// Endpoint 是 host[:port]，不含 scheme；Secure 决定 http 还是 https。
	Endpoint string
	// Secure 为 true 时用 https 连接 Endpoint。
	Secure    bool
	Bucket    string
	Prefix    string
	Region    string
	AccessKey string
	SecretKey string
}

// s3UploadConcurrency 限制同时上传的对象数：每个上传在内存里最多持有一个
// PartSize 的缓冲（16 MiB，见 s3PartSize），Pod 的内存上限只有 256 MiB。
const s3UploadConcurrency = 4

// s3PartSize 是多段上传的分片大小：16 MiB 保证 5 GiB 以内最多 320 片，
// 远低于 S3 的 10000 片上限，同时把每个上传的内存占用压在 16 MiB。
const s3PartSize = 16 << 20

// s3BlobStore 把 blob 存到 S3 兼容对象存储：对象 key 是
// <prefix>/<sha 前 2 位>/<sha 第 3-4 位>/<sha>，按内容寻址。
type s3BlobStore struct {
	client    *minio.Client
	bucket    string
	prefix    string
	uploadSem chan struct{}
}

func newS3BlobStore(cfg S3Config) (*s3BlobStore, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("S3 endpoint and bucket are required")
	}
	if cfg.Region == "" {
		// 自建网关一般没有区域概念；给一个默认值，避免签名时缺字段。
		cfg.Region = "us-east-1"
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	// 启动时确认桶存在：配错桶名或凭据时当场失败，别等第一次上传才发现。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %q: %w", cfg.Bucket, err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket %q does not exist; create it before starting", cfg.Bucket)
	}
	return &s3BlobStore{
		client:    client,
		bucket:    cfg.Bucket,
		prefix:    strings.Trim(cfg.Prefix, "/"),
		uploadSem: make(chan struct{}, s3UploadConcurrency),
	}, nil
}

func (s *s3BlobStore) Kind() string { return "s3" }

// key 按两层散列前缀布局对象：前 2 位 / 第 3-4 位 / 完整 sha。
// 调用方都用 validBlobSHA 校验过长度，这里可以安全切片。
func (s *s3BlobStore) key(sha string) string {
	key := sha[:2] + "/" + sha[2:4] + "/" + sha
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	return key
}

func (s *s3BlobStore) Head(ctx context.Context, sha string) (int64, error) {
	if err := validBlobSHA(sha); err != nil {
		return 0, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, s.key(sha), minio.StatObjectOptions{})
	if err != nil {
		if s3NotFound(err) {
			return 0, ErrBlobNotFound
		}
		return 0, err
	}
	return info.Size, nil
}

func (s *s3BlobStore) PutFile(ctx context.Context, sha, path string, size int64) error {
	if err := s.putPath(ctx, sha, path, size); err != nil {
		return err
	}
	// 内容已经进对象存储，临时文件留着没有意义；删不掉只是白占空间，不当作失败。
	if err := os.Remove(path); err != nil {
		slog.Warn("remove upload temp file after s3 put failed",
			slog.String("path", path), slog.Any("error", err))
	}
	return nil
}

// putPath 上传本地文件的内容，不删除源文件（迁移复用；PutFile 才消费临时文件）。
func (s *s3BlobStore) putPath(ctx context.Context, sha, path string, size int64) error {
	if err := validBlobSHA(sha); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open upload file: %w", err)
	}
	defer file.Close()

	s.uploadSem <- struct{}{}
	defer func() { <-s.uploadSem }()

	_, err = s.client.PutObject(ctx, s.bucket, s.key(sha), file, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		PartSize:    s3PartSize,
		NumThreads:  1,
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (s *s3BlobStore) Open(ctx context.Context, sha string) (io.ReadSeekCloser, error) {
	if err := validBlobSHA(sha); err != nil {
		return nil, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, s.key(sha), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	// 先 Stat 一次：blob 不存在时在写响应头之前就能回 404，
	// 而不是等 ServeContent 复制到一半才发现。Stat 顺便让 Object 缓存大小，
	// 后续 ServeContent 的 Seek(0, End) 不再额外发请求。
	if _, err := object.Stat(); err != nil {
		object.Close()
		if s3NotFound(err) {
			return nil, ErrBlobNotFound
		}
		return nil, fmt.Errorf("stat object: %w", err)
	}
	return object, nil
}

func (s *s3BlobStore) Delete(ctx context.Context, sha string) error {
	if err := validBlobSHA(sha); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, s.key(sha), minio.RemoveObjectOptions{}); err != nil {
		if s3NotFound(err) {
			return nil
		}
		return fmt.Errorf("remove object: %w", err)
	}
	return nil
}

// s3NotFound 判断错误是不是「对象不存在」。S3 网关对缺失对象回 404 + NoSuchKey；
// 桶不存在是配置错误，不能当成 blob 缺失。
func s3NotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchBucket" {
		return false
	}
	return resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey"
}
