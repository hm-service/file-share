package app_test

import (
	"errors"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"file-share/app"
	"file-share/core"
	"file-share/db"
	"file-share/internal/testenv"
)

// groupPaths 把分组行拆成路径列表：断言只关心路径与顺序。
func groupPaths(groups []db.Group) []string {
	paths := make([]string, 0, len(groups))
	for _, group := range groups {
		paths = append(paths, group.Path)
	}
	return paths
}

func samePaths(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

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

func TestOpenTightensDataLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	// DATA_DIR 由部署方交进来，必须先存在；这里用 0755 建出来，验证启动时被收紧。
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("准备 DATA_DIR 失败: %v", err)
	}
	instance, err := app.Open(app.Config{
		DataDir:           dataDir,
		SessionSigningKey: testenv.SessionSigningKey,
		AuthTokenHash:     "not-used-in-this-test",
		MaxFileSize:       testenv.DefaultMaxFileSize,
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
				MaxFileSize:       1024,
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
		MaxFileSize:       1024,
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
		{name: "缺 DATA_DIR", cfg: app.Config{SessionSigningKey: "key", AuthTokenHash: "hash", MaxFileSize: 1}},
		{name: "DATA_DIR 不存在", cfg: app.Config{DataDir: filepath.Join(t.TempDir(), "missing"), SessionSigningKey: "key", AuthTokenHash: "hash", MaxFileSize: 1}},
		{name: "文件大小上限为 0", cfg: app.Config{DataDir: t.TempDir(), SessionSigningKey: "key", AuthTokenHash: "hash", MaxFileSize: 0}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, err := app.Open(item.cfg); err == nil {
				t.Fatal("坏配置应当打不开")
			}
		})
	}
}

// DATA_DIR 缺了要当场报错，不顺手建一个把写错的部署掩盖过去。
func TestOpenRequiresExistingDataDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := app.Open(app.Config{
		DataDir:           missing,
		SessionSigningKey: testenv.SessionSigningKey,
		AuthTokenHash:     "hash",
		MaxFileSize:       1024,
	})
	if err == nil {
		t.Fatal("DATA_DIR 不存在时 Open() 应当报错")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("错误信息没点明目录不存在: %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("不该顺手创建 DATA_DIR: %v", statErr)
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
	if record.IsFavorite {
		t.Error("新记录默认不是收藏")
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
	instance := testenv.NewWith(t, func(cfg *app.Config) { cfg.MaxFileSize = 4 })
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
}

func TestDownload(t *testing.T) {
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

	// blob 丢失与记录不存在对外表现一致。
	testenv.RemoveBlob(t, instance, record.SHA256)
	if _, err := instance.OpenDownload(record.ID); err == nil {
		t.Fatal("blob 丢失时下载应当 404")
	} else {
		assertBusinessError(t, err, 404, "file.not-found")
	}
	if _, err := instance.OpenDownload("absent"); err == nil {
		t.Fatal("不存在的记录下载应当 404")
	}
}

func TestSignedDownloadURLs(t *testing.T) {
	instance := testenv.New(t)
	content := []byte("shared")
	record := testenv.SeedUpload(t, instance, "分享.txt", content)

	t.Run("分享链接", func(t *testing.T) {
		raw, err := instance.IssueShareLink(record.ID)
		if err != nil {
			t.Fatalf("IssueShareLink() 报错: %v", err)
		}
		path, sig, exp := parseSigned(t, raw)
		if path != "/s/"+record.ID {
			t.Errorf("路径 = %q，想要 /s/%s", path, record.ID)
		}
		if ttl := time.Until(time.Unix(exp, 0)); ttl <= 0 || ttl > core.ShareLinkTTL {
			t.Errorf("有效期剩余 %s，想要 (0, %s]", ttl, core.ShareLinkTTL)
		}

		download, err := instance.OpenSignedDownload(record.ID, "GET", path, sig, exp)
		if err != nil {
			t.Fatalf("OpenSignedDownload() 报错: %v", err)
		}
		defer download.Close()
		body, err := os.ReadFile(download.Blob.Name())
		if err != nil || string(body) != string(content) {
			t.Fatalf("blob 内容 = %q err=%v", body, err)
		}
		// HEAD 与 GET 共用签名。
		head, err := instance.OpenSignedDownload(record.ID, "HEAD", path, sig, exp)
		if err != nil {
			t.Errorf("HEAD 应当通过: %v", err)
		} else {
			head.Close()
		}

		// 改路径、改过期时间、换文件都验不过；过期也拒绝。
		if _, err := instance.OpenSignedDownload(record.ID, "GET", "/s/other", sig, exp); err == nil {
			t.Error("换路径应当 404")
		} else {
			assertBusinessError(t, err, 404, "download.invalid")
		}
		if _, err := instance.OpenSignedDownload(record.ID, "GET", path, sig, exp+60); err == nil {
			t.Error("改过期时间应当 404")
		} else {
			assertBusinessError(t, err, 404, "download.invalid")
		}
		if _, err := instance.OpenSignedDownload("absent", "GET", "/s/absent", sig, exp); err == nil {
			t.Error("换文件应当 404")
		} else {
			assertBusinessError(t, err, 404, "download.invalid")
		}
	})

	t.Run("下载地址", func(t *testing.T) {
		raw, err := instance.IssueDownloadURL(record.ID)
		if err != nil {
			t.Fatalf("IssueDownloadURL() 报错: %v", err)
		}
		path, sig, exp := parseSigned(t, raw)
		if path != "/s/"+record.ID {
			t.Errorf("路径 = %q，想要 /s/%s", path, record.ID)
		}
		if exp%3600 != 0 {
			t.Errorf("exp = %d，想要 UTC 整点", exp)
		}
		if now := time.Now().Unix(); exp <= now || exp > now+3600 {
			t.Errorf("exp = %d，距当前 %d 秒，想要 (0, 3600]", exp, exp-now)
		}
		download, err := instance.OpenSignedDownload(record.ID, "GET", path, sig, exp)
		if err != nil {
			t.Fatalf("OpenSignedDownload() 报错: %v", err)
		}
		download.Close()
	})

	t.Run("记录不存在或 blob 丢失时不签", func(t *testing.T) {
		if _, err := instance.IssueShareLink("absent"); err == nil {
			t.Fatal("不存在的记录签分享链接应当 404")
		} else {
			assertBusinessError(t, err, 404, "file.not-found")
		}
		if _, err := instance.IssueDownloadURL("absent"); err == nil {
			t.Fatal("不存在的记录签下载地址应当 404")
		} else {
			assertBusinessError(t, err, 404, "file.not-found")
		}
		testenv.RemoveBlob(t, instance, record.SHA256)
		if _, err := instance.IssueShareLink(record.ID); err == nil {
			t.Fatal("blob 丢失时签分享链接应当 404")
		} else {
			assertBusinessError(t, err, 404, "file.not-found")
		}
		if _, err := instance.IssueDownloadURL(record.ID); err == nil {
			t.Fatal("blob 丢失时签下载地址应当 404")
		} else {
			assertBusinessError(t, err, 404, "file.not-found")
		}
	})
}

// parseSigned 拆出签名 URL 的路径、签名与过期时间，供断言使用。
func parseSigned(t *testing.T, raw string) (path, sig string, exp int64) {
	t.Helper()
	parsed, err := neturl.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) 报错: %v", raw, err)
	}
	exp, err = strconv.ParseInt(parsed.Query().Get("exp"), 10, 64)
	if err != nil {
		t.Fatalf("exp 不是整数: %v", err)
	}
	return parsed.Path, parsed.Query().Get("sig"), exp
}

func TestUpdateFile(t *testing.T) {
	instance := testenv.New(t)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))

	before, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	maxSort := before[len(before)-1].SortOrder

	// 只改名：顺序不动。
	updated, err := instance.UpdateFile(first.ID, stringPtr("改名后.txt"), nil)
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.Filename != "改名后.txt" || updated.SortOrder != before[0].SortOrder {
		t.Fatalf("只改名后 = %+v，想要新名字且 sort_order 不变", updated)
	}

	// 改分组：分组名会 trim，记录被移到末尾。
	updated, err = instance.UpdateFile(first.ID, nil, stringPtr("  工作  "))
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.ID != first.ID || updated.GroupPath != "工作" || updated.SortOrder != maxSort+1 {
		t.Fatalf("UpdateFile() = %+v，想要分组「工作」且 sort_order %d", updated, maxSort+1)
	}
	files, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if len(files) != 2 || files[0].ID != second.ID || files[1].ID != first.ID {
		t.Fatalf("改分组后列表 = %+v，想要 first 在最后", files)
	}

	// 空串 = 未分组，仍然移到末尾。
	updated, err = instance.UpdateFile(first.ID, nil, stringPtr(""))
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.GroupPath != "" {
		t.Errorf("清空分组后 GroupPath = %q，想要空串", updated.GroupPath)
	}

	// 非法分组名：超长 / 控制字符。
	for name, value := range map[string]string{
		"超长":   strings.Repeat("a", core.MaxGroupNameBytes+1),
		"控制字符": "a\nb",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := instance.UpdateFile(first.ID, nil, stringPtr(value))
			assertBusinessError(t, err, 400, "group.invalid")
		})
	}

	// 非法文件名：空串 / 路径分隔符 / 控制字符。
	for name, value := range map[string]string{
		"空串":   "",
		"路径":   "a/b.txt",
		"控制字符": "a\nb.txt",
	} {
		t.Run("文件名"+name, func(t *testing.T) {
			_, err := instance.UpdateFile(first.ID, stringPtr(value), nil)
			assertBusinessError(t, err, 400, "upload.invalid-filename")
		})
	}

	if _, err := instance.UpdateFile(first.ID, nil, nil); err == nil {
		t.Fatal("两个字段都不给应当报错")
	} else {
		assertBusinessError(t, err, 400, "request.empty")
	}
	if _, err := instance.UpdateFile("absent", stringPtr("x.txt"), nil); err == nil {
		t.Fatal("不存在的记录应当报错")
	} else {
		assertBusinessError(t, err, 404, "file.not-found")
	}
}

// stringPtr 让测试里能直接写「改这个字段」；nil 表示不动。
func stringPtr(value string) *string { return &value }

func TestSetOrder(t *testing.T) {
	instance := testenv.New(t)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	third := testenv.SeedUpload(t, instance, "third.txt", []byte("third"))

	orderIDs := func() []string {
		files, err := instance.ListFiles()
		if err != nil {
			t.Fatalf("ListFiles() 报错: %v", err)
		}
		ids := make([]string, 0, len(files))
		for _, file := range files {
			ids = append(ids, file.ID)
		}
		return ids
	}

	items := []core.OrderItem{
		{ID: third.ID, SortOrder: 10},
		{ID: first.ID, SortOrder: 20},
		{ID: second.ID, SortOrder: 30},
	}
	if err := instance.SetOrder(items); err != nil {
		t.Fatalf("SetOrder() 报错: %v", err)
	}
	if got := orderIDs(); len(got) != 3 || got[0] != third.ID || got[1] != first.ID || got[2] != second.ID {
		t.Fatalf("排序后列表 = %v，想要 [third first second]", got)
	}

	// 任一条记录不存在：整体 404，原顺序不变。
	if err := instance.SetOrder([]core.OrderItem{
		{ID: first.ID, SortOrder: 1},
		{ID: "absent", SortOrder: 2},
	}); err == nil {
		t.Fatal("不存在的记录应当报错")
	} else {
		assertBusinessError(t, err, 404, "file.not-found")
	}
	if got := orderIDs(); got[0] != third.ID || got[1] != first.ID || got[2] != second.ID {
		t.Fatalf("回滚后列表 = %v，想要原顺序", got)
	}

	// 非法输入。
	invalidCases := map[string][]core.OrderItem{
		"空列表":   nil,
		"重复 id": {{ID: first.ID, SortOrder: 1}, {ID: first.ID, SortOrder: 2}},
		"空 id":  {{ID: "", SortOrder: 1}},
		"负数":    {{ID: first.ID, SortOrder: -1}},
		"超过上界":  {{ID: first.ID, SortOrder: core.MaxSortOrder + 1}},
	}
	for name, bad := range invalidCases {
		t.Run(name, func(t *testing.T) {
			assertBusinessError(t, instance.SetOrder(bad), 400, "order.invalid")
		})
	}
}

func TestRenameGroup(t *testing.T) {
	instance := testenv.New(t)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	kept := testenv.SeedUpload(t, instance, "kept.txt", []byte("kept"))
	for _, file := range []*db.File{first, second} {
		if _, err := instance.UpdateFile(file.ID, nil, stringPtr("旧组")); err != nil {
			t.Fatalf("UpdateFile() 报错: %v", err)
		}
	}
	if _, err := instance.UpdateFile(kept.ID, nil, stringPtr("保留组")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}

	// 组名会 trim；组内顺序保持（second 在前、first 在后）。
	if err := instance.RenameGroup("  旧组  ", " 新组 "); err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	files, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	byID := make(map[string]string, len(files))
	for _, file := range files {
		byID[file.ID] = file.GroupPath
	}
	if byID[first.ID] != "新组" || byID[second.ID] != "新组" || byID[kept.ID] != "保留组" {
		t.Fatalf("重命名后分组 = %+v，想要 first/second 在新组", byID)
	}

	// 目标分组已存在：409，且不合并。
	if err := instance.RenameGroup("新组", "保留组"); err == nil {
		t.Fatal("目标分组已存在应当报错")
	} else {
		assertBusinessError(t, err, 409, "group.exists")
	}
	// 源分组不存在：404。
	if err := instance.RenameGroup("不存在", "任意"); err == nil {
		t.Fatal("源分组不存在应当报错")
	} else {
		assertBusinessError(t, err, 404, "group.not-found")
	}
	// 空名 / 超长 / 控制字符都按非法分组名处理。
	for name, pair := range map[string][2]string{
		"旧名为空":   {"", "新组"},
		"新名为空":   {"新组", ""},
		"旧名超长":   {strings.Repeat("a", core.MaxGroupNameBytes+1), "新组"},
		"新名控制字符": {"新组", "a\nb"},
	} {
		t.Run(name, func(t *testing.T) {
			assertBusinessError(t, instance.RenameGroup(pair[0], pair[1]), 400, "group.invalid")
		})
	}
}

func TestGroupOrder(t *testing.T) {
	instance := testenv.New(t)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	if groups, err := instance.ListGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("初始分组 = %v, %v，想要空", groupPaths(groups), err)
	}
	for _, item := range []struct {
		id    string
		group string
	}{
		{first.ID, "一组"},
		{second.ID, "二组"},
	} {
		if _, err := instance.UpdateFile(item.id, nil, stringPtr(item.group)); err != nil {
			t.Fatalf("UpdateFile() 报错: %v", err)
		}
	}
	if groups, err := instance.ListGroups(); err != nil || !samePaths(groupPaths(groups), "一组", "二组") {
		t.Fatalf("ListGroups() = %v, %v，想要 [一组 二组]", groupPaths(groups), err)
	}

	if err := instance.SetGroupOrder("", []string{"二组", "一组"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}
	if groups, err := instance.ListGroups(); err != nil || !samePaths(groupPaths(groups), "二组", "一组") {
		t.Fatalf("重排后 ListGroups() = %v, %v，想要 [二组 一组]", groupPaths(groups), err)
	}

	// 名字不合法 / 重复：400。
	for name, names := range map[string][]string{
		"空列表里的空名": {""},
		"重复":      {"二组", "二组"},
		"带空白":     {" 二组"},
	} {
		t.Run(name, func(t *testing.T) {
			assertBusinessError(t, instance.SetGroupOrder("", names), 400, "order.invalid")
		})
	}
	// 与当前分组集合对不上（少 / 多）：409，提示刷新后重试。
	for name, names := range map[string][]string{
		"少一个": {"二组"},
		"多一个": {"二组", "一组", "三组"},
	} {
		t.Run(name, func(t *testing.T) {
			assertBusinessError(t, instance.SetGroupOrder("", names), 409, "group.changed")
		})
	}
}

func TestCreateGroup(t *testing.T) {
	instance := testenv.New(t)
	if groups, err := instance.ListGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("初始分组 = %v, %v，想要空", groupPaths(groups), err)
	}
	name, err := instance.CreateGroup("  空组  ")
	if err != nil {
		t.Fatalf("CreateGroup() 报错: %v", err)
	}
	if name != "空组" {
		t.Errorf("CreateGroup() = %q，想要 trim 后的「空组」", name)
	}
	if groups, err := instance.ListGroups(); err != nil || !samePaths(groupPaths(groups), "空组") {
		t.Fatalf("ListGroups() = %v, %v，想要 [空组]", groupPaths(groups), err)
	}

	// 空分组也能改名、参与排序。
	if err := instance.RenameGroup("空组", "改名空组"); err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	if groups, err := instance.ListGroups(); err != nil || !samePaths(groupPaths(groups), "改名空组") {
		t.Fatalf("改名后 ListGroups() = %v, %v", groupPaths(groups), err)
	}
	if err := instance.SetGroupOrder("", []string{"改名空组"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}

	// 同名 / 非法名。
	if _, err := instance.CreateGroup("改名空组"); err == nil {
		t.Fatal("同名分组应当报错")
	} else {
		assertBusinessError(t, err, 409, "group.exists")
	}
	for name, raw := range map[string]string{
		"空名":   "",
		"超长":   strings.Repeat("a", core.MaxGroupNameBytes+1),
		"控制字符": "a\nb",
		"系统路径": "favorites",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := instance.CreateGroup(raw)
			assertBusinessError(t, err, 400, "group.invalid")
		})
	}
}

// DeleteGroup 删分组：子分组级联；deleteFiles 决定组内文件一起删还是上移到上级。
func TestDeleteGroup(t *testing.T) {
	instance := testenv.New(t)
	first := testenv.SeedUpload(t, instance, "first.txt", []byte("first"))
	second := testenv.SeedUpload(t, instance, "second.txt", []byte("second"))
	if _, err := instance.UpdateFile(first.ID, nil, stringPtr("工作/项目A")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if _, err := instance.UpdateFile(second.ID, nil, stringPtr("工作")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}

	// 目录不存在 / 空路径。
	if err := instance.DeleteGroup("不存在", false); err == nil {
		t.Fatal("删不存在的分组应当报错")
	} else {
		assertBusinessError(t, err, 404, "group.not-found")
	}
	if err := instance.DeleteGroup("", false); err == nil {
		t.Fatal("空路径应当报错")
	} else {
		assertBusinessError(t, err, 400, "group.invalid")
	}

	// 保留文件：子分组级联删除，文件回到未分组，blob 仍可下载。
	if err := instance.DeleteGroup("工作", false); err != nil {
		t.Fatalf("DeleteGroup() 报错: %v", err)
	}
	if groups, err := instance.ListGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("ListGroups() = %v, %v，想要空", groupPaths(groups), err)
	}
	files, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	for _, file := range files {
		if file.GroupPath != "" {
			t.Errorf("%s 的 group_path = %q，想要空串", file.ID, file.GroupPath)
		}
	}
	if _, err := instance.OpenDownload(first.ID); err != nil {
		t.Errorf("保留文件后应当仍能读 blob: %v", err)
	}
}
