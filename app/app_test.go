package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"file-share/app"
	"file-share/core"
	"file-share/internal/testenv"
)

func assertBusinessError(t *testing.T, err error, status int, key string) {
	t.Helper()
	var business *core.Error
	if !errors.As(err, &business) {
		t.Fatalf("错误 %v 不是 *core.Error", err)
	}
	if business.Status != status || business.Key != key {
		t.Fatalf("业务错误 = %d %q，想要 %d %q", business.Status, business.Key, status, key)
	}
}

func TestOpenCreatesDataLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	instance, err := app.Open(app.Config{
		DataDir:           dataDir,
		SessionSigningKey: testenv.SessionSigningKey,
		AuthTokenHash:     "not-used-in-this-test",
		MaxUploadBytes:    testenv.DefaultMaxUploadBytes,
	})
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	defer instance.Close()

	for _, item := range []struct {
		path string
		perm os.FileMode
	}{
		{path: dataDir, perm: 0o700},
		{path: filepath.Join(dataDir, "files"), perm: 0o700},
		{path: filepath.Join(dataDir, "file-share.db"), perm: 0o600},
	} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatalf("stat %s 失败: %v", item.path, err)
		}
		if info.Mode().Perm() != item.perm {
			t.Errorf("%s 权限 = %04o，想要 %04o", item.path, info.Mode().Perm(), item.perm)
		}
	}
	if err := instance.Ready(); err != nil {
		t.Errorf("刚打开的实例应当就绪: %v", err)
	}
}

func TestOpenRejectsLegacyData(t *testing.T) {
	for _, legacy := range []string{"files.db", "favorite.json"} {
		t.Run(legacy, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dataDir, legacy), []byte("old"), 0o600); err != nil {
				t.Fatalf("准备旧数据失败: %v", err)
			}
			_, err := app.Open(app.Config{
				DataDir:           dataDir,
				SessionSigningKey: testenv.SessionSigningKey,
				AuthTokenHash:     "hash",
				MaxUploadBytes:    1024,
			})
			if err == nil {
				t.Fatalf("发现旧数据 %s 时 Open() 应当报错", legacy)
			}
			var business *core.Error
			if errors.As(err, &business) {
				t.Fatalf("启动错误不该是业务错误: %v", err)
			}
			// fail fast：发现旧数据的这一刻还没建新库，不能把新库混进旧目录。
			if _, statErr := os.Stat(filepath.Join(dataDir, "file-share.db")); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("旧数据检查之前不该建出新库: %v", statErr)
			}
		})
	}
}

func TestOpenCleansAndTightensFiles(t *testing.T) {
	dataDir := t.TempDir()
	filesDir := filepath.Join(dataDir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatalf("准备 files 目录失败: %v", err)
	}
	leftover := filepath.Join(filesDir, ".upload-leftover.tmp")
	if err := os.WriteFile(leftover, []byte("partial"), 0o600); err != nil {
		t.Fatalf("准备残留临时文件失败: %v", err)
	}
	sha := testenv.SHA256([]byte("existing blob"))
	blob := filepath.Join(filesDir, sha)
	if err := os.WriteFile(blob, []byte("existing blob"), 0o644); err != nil {
		t.Fatalf("准备 blob 失败: %v", err)
	}

	instance, err := app.Open(app.Config{
		DataDir:           dataDir,
		SessionSigningKey: testenv.SessionSigningKey,
		AuthTokenHash:     "hash",
		MaxUploadBytes:    1024,
	})
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	defer instance.Close()

	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("残留临时文件应当被清掉: %v", err)
	}
	info, err := os.Stat(blob)
	if err != nil {
		t.Fatalf("stat blob 失败: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("blob 权限 = %04o，想要 0600", info.Mode().Perm())
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  app.Config
	}{
		{name: "缺 DATA_DIR", cfg: app.Config{SessionSigningKey: "key", AuthTokenHash: "hash", MaxUploadBytes: 1}},
		{name: "上传上限为 0", cfg: app.Config{DataDir: t.TempDir(), SessionSigningKey: "key", AuthTokenHash: "hash", MaxUploadBytes: 0}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, err := app.Open(item.cfg); err == nil {
				t.Fatal("坏配置应当打不开")
			}
		})
	}
}

func TestReadyDetectsRelaxedPermissions(t *testing.T) {
	instance := testenv.New(t)
	if err := instance.Ready(); err != nil {
		t.Fatalf("Ready() 报错: %v", err)
	}

	if err := os.Chmod(instance.Config.DataDir, 0o755); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(instance.Config.DataDir, 0o700) })
	if err := instance.Ready(); err == nil {
		t.Error("数据目录权限被放松后 Ready() 应当失败")
	}
}

func TestReadyAuthorized(t *testing.T) {
	instance := testenv.NewWith(t, func(cfg *app.Config) { cfg.ReadyzToken = testenv.ReadyzToken })
	if instance.ReadyAuthorized("") {
		t.Error("配置了 tokoken 时空 token 不该通过")
	}
	if instance.ReadyAuthorized("wrong-token") {
		t.Error("错误 token 不该通过")
	}
	if !instance.ReadyAuthorized(testenv.ReadyzToken) {
		t.Error("正确 token 应当通过")
	}

	open := testenv.New(t)
	if !open.ReadyAuthorized("") {
		t.Error("没配置 token 时探针应当公开")
	}
}

func TestAuthenticate(t *testing.T) {
	instance := testenv.New(t)

	start := time.Now()
	if !instance.Authenticate(testenv.AuthToken) {
		t.Fatal("正确令牌应当通过")
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("登录校验耗时 %v，应当至少 1s", elapsed)
	}

	start = time.Now()
	if instance.Authenticate("wrong_token_000000") {
		t.Fatal("错误令牌不该通过")
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("失败校验耗时 %v，应当至少 1s（抹掉时序侧信道）", elapsed)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	instance := testenv.New(t)
	token, expiresAt, err := instance.IssueSession()
	if err != nil {
		t.Fatalf("IssueSession() 报错: %v", err)
	}
	if expiresAt <= time.Now().Unix() {
		t.Errorf("expires_at = %d，应当在未来", expiresAt)
	}
	if err := instance.VerifySession(token); err != nil {
		t.Errorf("签发出来的会话应当有效: %v", err)
	}
	if err := instance.VerifySession(token + "x"); !errors.Is(err, core.ErrSessionMalformed) {
		t.Errorf("被篡改的会话应当报格式无效，得到 %v", err)
	}
	if err := instance.VerifySession(testenv.ExpiredToken(t, instance)); !errors.Is(err, core.ErrSessionExpired) {
		t.Errorf("过期会话应当报过期，得到 %v", err)
	}
}

func TestUploadAndInstantReuse(t *testing.T) {
	instance := testenv.New(t)
	content := []byte("hello go file share")

	record := testenv.SeedUpload(t, instance, "hello.txt", content)
	if record.ID == "" || record.SHA256 != testenv.SHA256(content) || record.Size != int64(len(content)) {
		t.Fatalf("记录字段不对: %+v", record)
	}
	if record.IsFavorite || record.IsPublic {
		t.Error("新记录默认既非收藏也非公开")
	}
	blob, err := os.ReadFile(testenv.BlobPath(instance, record.SHA256))
	if err != nil {
		t.Fatalf("读 blob 失败: %v", err)
	}
	if string(blob) != string(content) {
		t.Errorf("blob 内容 = %q", blob)
	}

	// 秒传：同一个 blob 再补一条记录，不产生第二个物理文件。
	instant, exists, err := instance.CheckFile(record.SHA256, "copy.txt")
	if err != nil {
		t.Fatalf("CheckFile() 报错: %v", err)
	}
	if !exists || instant.ID == record.ID || instant.Size != record.Size {
		t.Fatalf("秒传结果不对: exists=%v record=%+v", exists, instant)
	}
	files, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("列表长度 = %d，想要 2（两条记录指向同一个 blob）", len(files))
	}
}

func TestCheckFileValidation(t *testing.T) {
	instance := testenv.New(t)
	cases := []struct {
		name string
		sha  string
		file string
		key  string
	}{
		{name: "缺 sha", sha: "", file: "a.txt", key: "upload.missing"},
		{name: "缺文件名", sha: testenv.SHA256([]byte("x")), file: "", key: "upload.missing"},
		{name: "sha 格式错", sha: "not-a-sha", file: "a.txt", key: "upload.invalid-sha256"},
		{name: "文件名带路径", sha: testenv.SHA256([]byte("x")), file: "dir/a.txt", key: "upload.invalid-filename"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			_, _, err := instance.CheckFile(item.sha, item.file)
			assertBusinessError(t, err, 400, item.key)
		})
	}

	// 不存在的 blob：exists=false 不是错误。
	_, exists, err := instance.CheckFile(testenv.SHA256([]byte("never uploaded")), "a.txt")
	if err != nil || exists {
		t.Fatalf("不存在的 blob 应当 exists=false: exists=%v err=%v", exists, err)
	}
}

func TestFinishUploadErrorPathsCleanTempFiles(t *testing.T) {
	instance := testenv.New(t)
	content := []byte("payload")

	cases := []struct {
		name     string
		filename string
		sha      string
		key      string
	}{
		{name: "sha 不匹配", filename: "a.txt", sha: testenv.SHA256([]byte("other")), key: "upload.checksum-mismatch"},
		{name: "sha 格式错", filename: "a.txt", sha: "zzz", key: "upload.invalid-sha256"},
		{name: "sha 缺失", filename: "a.txt", sha: "", key: "upload.missing"},
		{name: "文件名为空", filename: "", sha: testenv.SHA256(content), key: "upload.missing"},
		{name: "文件名带斜杠", filename: "dir/a.txt", sha: testenv.SHA256(content), key: "upload.invalid-filename"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			upload, err := instance.NewUpload()
			if err != nil {
				t.Fatalf("NewUpload() 报错: %v", err)
			}
			if _, err := upload.Write(content); err != nil {
				t.Fatalf("Write() 报错: %v", err)
			}
			_, err = instance.FinishUpload(upload, item.filename, item.sha)
			assertBusinessError(t, err, 400, item.key)
			if leftovers := testenv.UploadTempFiles(t, instance); len(leftovers) != 0 {
				t.Errorf("错误路径不该留下临时文件: %v", leftovers)
			}
		})
	}
}

func TestFinishUploadTooBig(t *testing.T) {
	instance := testenv.NewWith(t, func(cfg *app.Config) { cfg.MaxUploadBytes = 4 })
	upload, err := instance.NewUpload()
	if err != nil {
		t.Fatalf("NewUpload() 报错: %v", err)
	}
	defer upload.Abort()
	_, writeErr := upload.Write([]byte("12345"))
	if writeErr == nil {
		t.Fatal("超过上限的写入应当报错")
	}
	assertBusinessError(t, writeErr, 413, "upload.too-big")
}

func TestDeleteKeepsBlobWhileReferenced(t *testing.T) {
	instance := testenv.New(t)
	content := []byte("shared content")
	first := testenv.SeedUpload(t, instance, "one.txt", content)
	second := testenv.SeedUpload(t, instance, "two.txt", content)
	if first.SHA256 != second.SHA256 {
		t.Fatal("同内容上传应当指向同一个 sha")
	}
	sha := first.SHA256

	if err := instance.DeleteFile(first.ID); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if !testenv.BlobExists(instance, sha) {
		t.Fatal("还有记录引用时 blob 不该被删")
	}
	if err := instance.DeleteFile(second.ID); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if testenv.BlobExists(instance, sha) {
		t.Fatal("最后一条引用删掉后 blob 应当被删")
	}

	assertBusinessError(t, instance.DeleteFile(first.ID), 404, "file.not-found")
}

func TestToggleMissingFile(t *testing.T) {
	instance := testenv.New(t)
	_, err := instance.ToggleFavorite("absent")
	assertBusinessError(t, err, 404, "file.not-found")
	_, err = instance.TogglePublic("absent")
	assertBusinessError(t, err, 404, "file.not-found")
}

func TestDownloadVisibility(t *testing.T) {
	instance := testenv.New(t)
	content := []byte("download me")
	record := testenv.SeedUpload(t, instance, "下载.txt", content)

	download, err := instance.OpenDownload(record.ID)
	if err != nil {
		t.Fatalf("OpenDownload() 报错: %v", err)
	}
	defer download.Close()
	body, err := os.ReadFile(download.Blob.Name())
	if err != nil || string(body) != string(content) {
		t.Fatalf("blob 内容 = %q err=%v", body, err)
	}

	if _, err := instance.OpenPublicDownload(record.ID); err == nil {
		t.Fatal("私有记录的公开下载应当 404")
	} else {
		assertBusinessError(t, err, 404, "file.not-found")
	}
	if _, err := instance.TogglePublic(record.ID); err != nil {
		t.Fatalf("TogglePublic() 报错: %v", err)
	}
	public, err := instance.OpenPublicDownload(record.ID)
	if err != nil {
		t.Fatalf("公开后 OpenPublicDownload() 报错: %v", err)
	}
	public.Close()

	// blob 丢失与记录不存在对外表现一致。
	testenv.RemoveBlob(t, instance, record.SHA256)
	if _, err := instance.OpenDownload(record.ID); err == nil {
		t.Fatal("blob 丢失时下载应当 404")
	} else {
		assertBusinessError(t, err, 404, "file.not-found")
	}
	if _, err := instance.OpenPublicDownload(record.ID); err == nil {
		t.Fatal("blob 丢失时公开下载应当 404")
	}
	if _, err := instance.OpenDownload("absent"); err == nil {
		t.Fatal("不存在的记录下载应当 404")
	}
}
