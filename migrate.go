package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"file-share/app"
)

// runMigrateBlobs 是 `app migrate-blobs [--dry-run]`：把本地 blob 搬到 S3。
// 它只读 DATA_DIR 与 S3 配置，不需要 AUTH_TOKEN 等运行期 Secret，方便在停服窗口里执行。
func runMigrateBlobs(args []string) int {
	initLogger()
	flags := flag.NewFlagSet("migrate-blobs", flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "只检查并打印，不写对象存储")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		slog.Error("migrate-blobs takes no positional arguments")
		return 2
	}

	s3cfg, err := readS3Config()
	if err != nil {
		slog.Error("init failed", slog.Any("error", err))
		return 1
	}
	if s3cfg == nil {
		slog.Error("init failed", slog.String("error",
			"migrate-blobs requires S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY and S3_SECRET_KEY"))
		return 1
	}
	dataDir := readDataDir()

	// Ctrl-C 之后不再开始新的对象，正在传的那个会随 ctx 一起取消。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.MigrateToS3(ctx, dataDir, *s3cfg, *dryRun, os.Stdout); err != nil {
		slog.Error("migrate blobs failed", slog.Any("error", err), slog.String("data_dir", dataDir))
		return 1
	}
	slog.Info("migrate blobs finished",
		slog.String("data_dir", dataDir),
		slog.String("bucket", s3cfg.Bucket),
		slog.String("prefix", s3cfg.Prefix),
		slog.Bool("dry_run", *dryRun))
	return 0
}
