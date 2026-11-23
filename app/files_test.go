package app

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"file-share/core"
)

// newTestApp 起一个只读视图实例：DATA_DIR / FILES_DIR 都是临时目录。
func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := Config{
		DataDir:           t.TempDir(),
		FilesDir:          t.TempDir(),
		SessionSigningKey: "test_session_signing_key_0123456789",
		URLSignatureKey:   "test_url_signature_key_0123456789",
		Users: []User{{
			Name:        "tester",
			Password:    "tester_password_0123456789",
			Permissions: PermissionRead,
		}},
	}
	instance, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

func writeTestFile(t *testing.T, instance *App, rel, content string) {
	t.Helper()
	full := filepath.Join(instance.Config.FilesDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func TestScanListsFilesAndGroups(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "软件/安卓应用/app.apk", "apk")
	writeTestFile(t, instance, "软件/说明.md", "doc")
	writeTestFile(t, instance, "根目录.txt", "root")
	if err := os.MkdirAll(filepath.Join(instance.Config.FilesDir, "空目录", "子目录"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := instance.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("文件数 = %d，想要 3: %+v", len(files), files)
	}
	// 排序：先按分组路径，再按名称。
	if files[0].ID != "根目录.txt" {
		t.Errorf("第一个文件 = %+v，想要根目录.txt（分组路径空串排最前）", files[0])
	}
	byID := map[string]File{}
	for _, file := range files {
		byID[file.ID] = file
	}
	apkFile, ok := byID["软件/安卓应用/app.apk"]
	if !ok {
		t.Fatalf("缺少 软件/安卓应用/app.apk: %+v", byID)
	}
	if apkFile.Filename != "app.apk" || apkFile.GroupPath != "软件/安卓应用" || apkFile.Size != 3 {
		t.Errorf("记录字段不对: %+v", apkFile)
	}

	groups, err := instance.ListGroups()
	if err != nil {
		t.Fatalf("ListGroups() 报错: %v", err)
	}
	paths := map[string]bool{}
	for _, group := range groups {
		paths[group.Path] = true
	}
	for _, want := range []string{"软件", "软件/安卓应用", "空目录", "空目录/子目录"} {
		if !paths[want] {
			t.Errorf("缺少分组 %q: %+v", want, groups)
		}
	}
	// 目录深度不限制：三级目录也要出现。
	writeTestFile(t, instance, "a/b/c/d/deep.txt", "deep")
	groups, err = instance.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	deepFound := false
	for _, group := range groups {
		if group.Path == "a/b/c/d" {
			deepFound = true
		}
	}
	if !deepFound {
		t.Errorf("任意深度的目录应当出现: %+v", groups)
	}
}

func TestScanSkipsHiddenAndSymlinks(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "可见.txt", "v")
	writeTestFile(t, instance, ".隐藏文件.txt", "h")
	writeTestFile(t, instance, ".file-share/manifest.jsonl", "{}")
	writeTestFile(t, instance, "目录/文件.txt", "f")
	if err := os.Symlink(filepath.Join(instance.Config.FilesDir, "可见.txt"),
		filepath.Join(instance.Config.FilesDir, "链接.txt")); err != nil {
		t.Skipf("文件系统不支持符号链接: %v", err)
	}
	files, err := instance.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Filename == "链接.txt" || file.Filename == ".隐藏文件.txt" {
			t.Errorf("不应出现 %q: %+v", file.Filename, file)
		}
	}
	if len(files) != 2 {
		t.Errorf("文件数 = %d，想要 2: %+v", len(files), files)
	}
	groups, err := instance.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group.Name == ".file-share" {
			t.Errorf("隐藏目录不应出现: %+v", group)
		}
	}
}

func TestValidateRelPath(t *testing.T) {
	valid := []string{"a", "a/b", "软件/安卓应用/app.apk", "a b/c.txt"}
	for _, value := range valid {
		if err := validateRelPath(value); err != nil {
			t.Errorf("validateRelPath(%q) = %v，想要 nil", value, err)
		}
	}
	invalid := []string{"", ".", "/abs", "a/../b", "../x", "a/./b", "a//b", "a\\b", ".hidden", "a/.hidden/b"}
	for _, value := range invalid {
		if err := validateRelPath(value); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("validateRelPath(%q) = %v，想要 ErrInvalidPath", value, err)
		}
	}
}

func TestOpenDownload(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "软件/说明.md", "内容")
	download, err := instance.OpenDownload("软件/说明.md")
	if err != nil {
		t.Fatalf("OpenDownload() 报错: %v", err)
	}
	defer download.Close()
	if download.Name != "说明.md" || download.Size != int64(len("内容")) {
		t.Errorf("下载元数据不对: %+v", download)
	}
	if _, err := instance.OpenDownload("不存在.txt"); !errors.Is(err, ErrNotFound) && !isBusiness(err, 404) {
		t.Errorf("不存在文件 = %v，想要 404", err)
	}
	if _, err := instance.OpenDownload("../etc/passwd"); !errors.Is(err, ErrInvalidPath) && !isBusiness(err, 400) {
		t.Errorf("越界路径 = %v，想要 400", err)
	}
}

// isBusiness 判断错误是不是业务错误（core.Error），并核对状态码。
func isBusiness(err error, status int) bool {
	var business *core.Error
	return errors.As(err, &business) && business.Status == status
}

func TestFavoritesPersistAndToggle(t *testing.T) {
	instance := newTestApp(t)
	favorite, err := instance.ToggleFavorite("软件/说明.md")
	if err != nil || !favorite {
		t.Fatalf("ToggleFavorite() = %v, %v，想要 true, nil", favorite, err)
	}
	// 重新打开实例，收藏应当还能读回来。
	reopened, err := openFavorites(instance.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.All()["软件/说明.md"] {
		t.Errorf("收藏没有持久化: %+v", reopened.All())
	}
	favorite, err = instance.ToggleFavorite("软件/说明.md")
	if err != nil || favorite {
		t.Fatalf("再次切换 = %v, %v，想要 false, nil", favorite, err)
	}
	if _, err := instance.ToggleFavorite("../bad"); err == nil {
		t.Fatal("非法路径应当报错")
	}
	info, err := os.Stat(filepath.Join(instance.Config.DataDir, favoritesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("favorites.json 权限 = %o，想要 600", info.Mode().Perm())
	}
}

func TestSignedURLRoundTrip(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "软件/说明.md", "内容")
	link, err := instance.IssueShareLink("软件/说明.md", core.SignedURLTTL)
	if err != nil {
		t.Fatalf("IssueShareLink() 报错: %v", err)
	}
	escapedPath, sig, exp, ok := splitSignedURL(t, link)
	if !ok {
		t.Fatalf("分享链接格式不对: %q", link)
	}
	download, err := instance.OpenSignedDownload("软件/说明.md", "GET", escapedPath, sig, exp)
	if err != nil {
		t.Fatalf("OpenSignedDownload() 报错: %v", err)
	}
	download.Close()

	if _, err := instance.OpenSignedDownload("软件/说明.md", "GET", escapedPath, "bad-signature", exp); !isBusiness(err, 403) {
		t.Errorf("坏签名 = %v，想要 403", err)
	}
	if _, err := instance.IssueShareLink("软件/说明.md", 0); err != nil {
		t.Errorf("零 ttl 不应当在签发时报错: %v", err)
	}
}

func TestIssueSignedURLForMissingFile(t *testing.T) {
	instance := newTestApp(t)
	if _, err := instance.IssueDownloadURL("不存在.txt"); !isBusiness(err, 404) {
		t.Errorf("不存在文件 = %v，想要 404", err)
	}
}

func TestReady(t *testing.T) {
	instance := newTestApp(t)
	if err := instance.Ready(); err != nil {
		t.Fatalf("Ready() 报错: %v", err)
	}
}

func TestOpenRejectsMissingFilesDir(t *testing.T) {
	cfg := Config{
		DataDir:           t.TempDir(),
		FilesDir:          filepath.Join(t.TempDir(), "missing"),
		SessionSigningKey: "test_session_signing_key_0123456789",
		URLSignatureKey:   "test_url_signature_key_0123456789",
		Users: []User{{
			Name:        "tester",
			Password:    "tester_password_0123456789",
			Permissions: PermissionRead,
		}},
	}
	if _, err := Open(cfg); err == nil {
		t.Fatal("FILES_DIR 不存在时 Open() 应当报错")
	}
}

// splitSignedURL 把 /s/<escaped path>?sig=..&exp=.. 拆开，供 round-trip 测试使用。
func splitSignedURL(t *testing.T, link string) (escapedPath, sig string, exp int64, ok bool) {
	t.Helper()
	index := strings.Index(link, "?")
	if index < 0 {
		return "", "", 0, false
	}
	escapedPath = link[:index]
	values, err := url.ParseQuery(link[index+1:])
	if err != nil {
		return "", "", 0, false
	}
	exp, err = strconv.ParseInt(values.Get("exp"), 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	return escapedPath, values.Get("sig"), exp, true
}
