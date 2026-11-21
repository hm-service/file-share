package app

import (
	"context"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 连不上时 Check 必须报错：这是 /api/storage 把存储状态标成不可用的依据。
// 不走 newS3BlobStore（它启动时就要求桶可达），这里直接构造一个指向死端口的客户端。
func TestS3BlobStoreCheckUnreachable(t *testing.T) {
	client, err := minio.New("127.0.0.1:1", &minio.Options{
		Creds:  credentials.NewStaticV4("test_access_key", "test_secret_key", ""),
		Secure: false,
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("构造 S3 客户端失败: %v", err)
	}
	store := &s3BlobStore{client: client, bucket: "file-share"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.Check(ctx); err == nil {
		t.Fatal("S3 不可达时 Check 应返回错误")
	}
}
