package testenv

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"file-share/app"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 集成测试的环境变量：全部设置后才跑，否则相关用例 skip，CI 不需要对象存储。
const (
	s3TestEndpoint  = "S3_TEST_ENDPOINT"
	s3TestBucket    = "S3_TEST_BUCKET"
	s3TestAccessKey = "S3_TEST_ACCESS_KEY"
	s3TestSecretKey = "S3_TEST_SECRET_KEY"
	s3TestRegion    = "S3_TEST_REGION"
)

// S3Config 构造一套测试用 S3 配置；没配环境变量就跳过当前测试。
//
// 每个测试用独立的 key 前缀（file-share-test/<测试名>），开始前与结束后都清空，
// 避免用例之间互相影响，也不在共享桶里留垃圾。
func S3Config(t *testing.T) *app.S3Config {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv(s3TestEndpoint))
	if endpoint == "" {
		t.Skipf("%s 未设置，跳过 S3 集成测试", s3TestEndpoint)
	}
	bucket := strings.TrimSpace(os.Getenv(s3TestBucket))
	accessKey := strings.TrimSpace(os.Getenv(s3TestAccessKey))
	secretKey := strings.TrimSpace(os.Getenv(s3TestSecretKey))
	if bucket == "" || accessKey == "" || secretKey == "" {
		t.Fatalf("%s 已设置，但 %s / %s / %s 不完整", s3TestEndpoint, s3TestBucket, s3TestAccessKey, s3TestSecretKey)
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Host == "" {
		t.Fatalf("%s = %q 不是合法 URL", s3TestEndpoint, endpoint)
	}
	region := strings.TrimSpace(os.Getenv(s3TestRegion))
	if region == "" {
		region = "us-east-1"
	}
	cfg := &app.S3Config{
		Endpoint:  target.Host,
		Secure:    target.Scheme == "https",
		Bucket:    bucket,
		Prefix:    "file-share-test/" + sanitizeTestName(t.Name()),
		Region:    region,
		AccessKey: accessKey,
		SecretKey: secretKey,
	}
	// 先清一次：上一次失败退出可能留下对象。
	cleanupS3Prefix(t, cfg)
	t.Cleanup(func() { cleanupS3Prefix(t, cfg) })
	return cfg
}

// NewS3 起一套用 S3 存 blob 的实例；配置来自 S3_TEST_*，没配就 skip。
func NewS3(t *testing.T) (*app.App, *app.S3Config) {
	t.Helper()
	return NewS3With(t, nil)
}

// NewS3With 同上，但允许在打开前改 Config（例如放大单文件上限）。
func NewS3With(t *testing.T, mutate func(*app.Config)) (*app.App, *app.S3Config) {
	t.Helper()
	cfg := S3Config(t)
	instance := NewWith(t, func(c *app.Config) {
		c.S3 = cfg
		if mutate != nil {
			mutate(c)
		}
	})
	return instance, cfg
}

// S3Key 返回一个 blob 在桶里的完整 key（与实现的布局约定一致）。
func S3Key(cfg *app.S3Config, sha string) string {
	return cfg.Prefix + "/" + sha[:2] + "/" + sha[2:4] + "/" + sha
}

// S3Stat 返回对象大小与是否存在，供测试直接检查对象存储里的状态。
func S3Stat(t *testing.T, cfg *app.S3Config, sha string) (int64, bool) {
	t.Helper()
	client := s3Client(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := client.StatObject(ctx, cfg.Bucket, S3Key(cfg, sha), minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).StatusCode == 404 {
			return 0, false
		}
		t.Fatalf("S3 StatObject(%s) 报错: %v", S3Key(cfg, sha), err)
	}
	return info.Size, true
}

func s3Client(t *testing.T, cfg *app.S3Config) *minio.Client {
	t.Helper()
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
		Region: cfg.Region,
	})
	if err != nil {
		t.Fatalf("创建 S3 客户端失败: %v", err)
	}
	return client
}

func cleanupS3Prefix(t *testing.T, cfg *app.S3Config) {
	t.Helper()
	client := s3Client(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for object := range client.ListObjects(ctx, cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    cfg.Prefix + "/",
		Recursive: true,
	}) {
		if object.Err != nil {
			t.Logf("清理 S3 前缀 %s 时列对象失败: %v", cfg.Prefix, object.Err)
			return
		}
		if err := client.RemoveObject(ctx, cfg.Bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("清理 S3 对象 %s 失败: %v", object.Key, err)
		}
	}
}

// sanitizeTestName 把测试名变成合法的 key 前缀段。用确定的名字而不是随机后缀：
// 上一次失败留下对象时，下一次运行开始前会按同一个前缀清掉。
func sanitizeTestName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
