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
//
// 三个测试用户覆盖权限与可见范围的差别：
//   - tester：整个数据目录，可收藏；
//   - guest：整个数据目录，不可收藏；
//   - viewer：只授权 /目录A，可收藏。
func newTestApp(t *testing.T) *App {
	t.Helper()
	filesDir := t.TempDir()
	// 可见范围是启动时的硬约束：先把目录建出来，再让 Open 校验。
	for _, dir := range []string{"目录A", "目录B", "目录C"} {
		if err := os.MkdirAll(filepath.Join(filesDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		DataDir:           t.TempDir(),
		FilesDir:          filesDir,
		SessionSigningKey: "test_session_signing_key_0123456789",
		URLSignatureKey:   "test_url_signature_key_0123456789",
		Users: []User{
			{Name: "tester", Password: "tester_password_0123456789", Permissions: PermissionRead | PermissionFav, Access: []string{"/"}},
			{Name: "guest", Password: "guest_password_0123456789", Permissions: PermissionRead, Access: []string{"/"}},
			{Name: "viewer", Password: "viewer_password_0123456789", Permissions: PermissionRead | PermissionFav, Access: []string{"/目录A"}},
		},
	}
	instance, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

// userByName 取配置里的测试用户。
func userByName(t *testing.T, instance *App, name string) User {
	t.Helper()
	user, ok := instance.User(name)
	if !ok {
		t.Fatalf("缺少测试用户 %q", name)
	}
	return user
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
	tester := userByName(t, instance, "tester")
	writeTestFile(t, instance, "目录A/说明.md", "doc")
	writeTestFile(t, instance, "目录A/x.txt", "x")
	writeTestFile(t, instance, "根目录.txt", "root")
	if err := os.MkdirAll(filepath.Join(instance.Config.FilesDir, "空目录", "子目录"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := instance.ListFiles(tester)
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
	docFile, ok := byID["目录A/说明.md"]
	if !ok {
		t.Fatalf("缺少 目录A/说明.md: %+v", byID)
	}
	if docFile.Filename != "说明.md" || docFile.GroupPath != "目录A" || docFile.Size != 3 {
		t.Errorf("记录字段不对: %+v", docFile)
	}

	groups, err := instance.ListGroups(tester)
	if err != nil {
		t.Fatalf("ListGroups() 报错: %v", err)
	}
	paths := map[string]bool{}
	for _, group := range groups {
		paths[group.Path] = true
	}
	for _, want := range []string{"目录A", "目录B", "目录C", "空目录", "空目录/子目录"} {
		if !paths[want] {
			t.Errorf("缺少分组 %q: %+v", want, groups)
		}
	}
	// 目录深度不限制：三级目录也要出现。
	writeTestFile(t, instance, "a/b/c/d/deep.txt", "deep")
	groups, err = instance.ListGroups(tester)
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

// TestViewerSeesOnlyAuthorizedPaths 验证可见范围：授权目录之外的内容既不出现在列表里，
// 也签不出下载地址；用户看到的路径是数据目录下的完整路径，不是「以授权目录为根」的重写。
func TestViewerSeesOnlyAuthorizedPaths(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "目录A/说明.md", "doc")
	writeTestFile(t, instance, "目录A/子目录/inside.txt", "in")
	writeTestFile(t, instance, "目录B/secret.txt", "secret")
	writeTestFile(t, instance, "根目录.txt", "root")

	viewer := userByName(t, instance, "viewer")
	files, err := instance.ListFiles(viewer)
	if err != nil {
		t.Fatalf("ListFiles(viewer) 报错: %v", err)
	}
	ids := make([]string, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	if len(files) != 2 || ids[0] != "目录A/说明.md" || ids[1] != "目录A/子目录/inside.txt" {
		t.Errorf("受限用户文件列表 = %v，想要 目录A 下的完整路径", ids)
	}
	groups, err := instance.ListGroups(viewer)
	if err != nil {
		t.Fatal(err)
	}
	groupPaths := make([]string, 0, len(groups))
	for _, group := range groups {
		groupPaths = append(groupPaths, group.Path)
	}
	// 根视图里只有 目录A（及它自己的子目录），目录B、目录C 不出现。
	if len(groups) != 2 || groupPaths[0] != "目录A" || groupPaths[1] != "目录A/子目录" {
		t.Errorf("受限用户分组 = %v，想要 [目录A 目录A/子目录]", groupPaths)
	}

	// 授权目录之外：按「不存在」处理，不泄露它的存在。
	if _, err := instance.IssueDownloadURL(viewer, "目录B/secret.txt"); !isBusiness(err, 404) {
		t.Errorf("不可见路径 = %v，想要 404", err)
	}
	if _, err := instance.IssueDownloadURL(viewer, "../目录B/secret.txt"); !isBusiness(err, 400) {
		t.Errorf("../ 越权 = %v，想要 400", err)
	}
	if _, err := instance.IssueDownloadURL(viewer, "目录A/说明.md"); err != nil {
		t.Errorf("可见路径应当能签: %v", err)
	}
	// 管理员视角仍然是整个数据目录。
	tester := userByName(t, instance, "tester")
	adminFiles, err := instance.ListFiles(tester)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminFiles) != 4 {
		t.Errorf("管理员文件数 = %d，想要 4", len(adminFiles))
	}
}

// TestAccessKeepsAncestorChain 验证深层授权保留祖先链：access=["/a/b"] 时，
// 根视图要能看到 a（才知道自己处在哪一层），但 a 下除 b 以外的兄弟不出现。
func TestAccessKeepsAncestorChain(t *testing.T) {
	filesDir := t.TempDir()
	for _, dir := range []string{"a/b", "a/c"} {
		if err := os.MkdirAll(filepath.Join(filesDir, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		DataDir:           t.TempDir(),
		FilesDir:          filesDir,
		SessionSigningKey: "test_session_signing_key_0123456789",
		URLSignatureKey:   "test_url_signature_key_0123456789",
		Users: []User{{
			Name: "deep", Password: "deep_password_0123456789",
			Permissions: PermissionRead, Access: []string{"/a/b"},
		}},
	}
	instance, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	deep := userByName(t, instance, "deep")
	writeTestFile(t, instance, "a/b/x.txt", "x")
	writeTestFile(t, instance, "a/c/y.txt", "y")

	groups, err := instance.ListGroups(deep)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(groups))
	for _, group := range groups {
		paths = append(paths, group.Path)
	}
	if len(groups) != 2 || paths[0] != "a" || paths[1] != "a/b" {
		t.Errorf("分组 = %v，想要 [a a/b]（祖先链保留、兄弟不出现）", paths)
	}
	files, err := instance.ListFiles(deep)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != "a/b/x.txt" {
		t.Errorf("文件 = %+v，想要只有 a/b/x.txt", files)
	}
}

// TestMultiAccessUnion 验证多值可见范围取并集。
func TestMultiAccessUnion(t *testing.T) {
	filesDir := t.TempDir()
	for _, dir := range []string{"目录A", "目录B", "目录C"} {
		if err := os.MkdirAll(filepath.Join(filesDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		DataDir:           t.TempDir(),
		FilesDir:          filesDir,
		SessionSigningKey: "test_session_signing_key_0123456789",
		URLSignatureKey:   "test_url_signature_key_0123456789",
		Users: []User{{
			Name: "multi", Password: "multi_password_0123456789",
			Permissions: PermissionRead, Access: []string{"/目录A", "/目录C"},
		}},
	}
	instance, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	multi := userByName(t, instance, "multi")
	writeTestFile(t, instance, "目录A/a.txt", "a")
	writeTestFile(t, instance, "目录B/b.txt", "b")
	writeTestFile(t, instance, "目录C/c.txt", "c")

	files, err := instance.ListFiles(multi)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	if len(files) != 2 || ids[0] != "目录A/a.txt" || ids[1] != "目录C/c.txt" {
		t.Errorf("文件 = %v，想要 目录A 与 目录C 的并集", ids)
	}
	if _, err := instance.IssueDownloadURL(multi, "目录B/b.txt"); !isBusiness(err, 404) {
		t.Errorf("未授权目录 = %v，想要 404", err)
	}
}

// TestScanIncludesHiddenAndSkipsSymlinks 验证隐藏文件是普通数据（照常展示），
// 只有符号链接这类非常规文件被跳过。
func TestScanIncludesHiddenAndSkipsSymlinks(t *testing.T) {
	instance := newTestApp(t)
	tester := userByName(t, instance, "tester")
	writeTestFile(t, instance, "可见.txt", "v")
	writeTestFile(t, instance, ".隐藏文件.txt", "h")
	writeTestFile(t, instance, ".file-share/manifest.jsonl", "{}")
	writeTestFile(t, instance, "目录/文件.txt", "f")
	if err := os.Symlink(filepath.Join(instance.Config.FilesDir, "可见.txt"),
		filepath.Join(instance.Config.FilesDir, "链接.txt")); err != nil {
		t.Skipf("文件系统不支持符号链接: %v", err)
	}
	files, err := instance.ListFiles(tester)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, file := range files {
		found[file.ID] = true
	}
	for _, want := range []string{"可见.txt", ".隐藏文件.txt", ".file-share/manifest.jsonl", "目录/文件.txt"} {
		if !found[want] {
			t.Errorf("缺少 %q: %+v", want, found)
		}
	}
	if found["链接.txt"] {
		t.Error("符号链接不应出现")
	}
	if len(files) != 4 {
		t.Errorf("文件数 = %d，想要 4: %+v", len(files), files)
	}
	groups, err := instance.ListGroups(tester)
	if err != nil {
		t.Fatal(err)
	}
	hiddenDir := false
	for _, group := range groups {
		if group.Path == ".file-share" {
			hiddenDir = true
		}
	}
	if !hiddenDir {
		t.Error("隐藏目录 .file-share 应当作为普通分组出现")
	}
}

// TestSymlinkCannotBypassAccess 验证段级符号链接不能绕过可见范围：
// /目录A/链接 指向 /目录B 时，通过 /目录A/链接/secret.txt 打不开。
func TestSymlinkCannotBypassAccess(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "目录B/secret.txt", "secret")
	if err := os.Symlink(filepath.Join(instance.Config.FilesDir, "目录B"),
		filepath.Join(instance.Config.FilesDir, "目录A", "链接")); err != nil {
		t.Skipf("文件系统不支持符号链接: %v", err)
	}
	viewer := userByName(t, instance, "viewer")
	if _, err := instance.IssueDownloadURL(viewer, "目录A/链接/secret.txt"); !isBusiness(err, 404) {
		t.Errorf("符号链接穿越 = %v，想要 404", err)
	}
}

func TestValidateRelPath(t *testing.T) {
	valid := []string{"a", "a/b", "目录A/说明.md", "a b/c.txt", ".hidden", "a/.hidden/b"}
	for _, value := range valid {
		if err := validateRelPath(value); err != nil {
			t.Errorf("validateRelPath(%q) = %v，想要 nil", value, err)
		}
	}
	invalid := []string{"", ".", "/abs", "a/../b", "../x", "a/./b", "a//b", "a\\b"}
	for _, value := range invalid {
		if err := validateRelPath(value); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("validateRelPath(%q) = %v，想要 ErrInvalidPath", value, err)
		}
	}
}

func TestNormalizeAccess(t *testing.T) {
	access, err := normalizeAccess([]string{"/", "/目录A/"})
	if err == nil {
		t.Errorf(`access ["/", "/目录A/"] 应当报错（"/" 已覆盖全部），得到 %v`, access)
	}
	access, err = normalizeAccess([]string{"/目录A", "/目录A/"})
	if err == nil {
		t.Errorf("规范化后重复的 access 应当报错，得到 %v", access)
	}
	access, err = normalizeAccess([]string{"/目录A/", "/目录B/子"})
	if err != nil {
		t.Fatalf("合法 access 报错: %v", err)
	}
	if len(access) != 2 || access[0] != "/目录A" || access[1] != "/目录B/子" {
		t.Errorf("access = %v，想要规范化后的 [/目录A /目录B/子]", access)
	}
	access, err = normalizeAccess([]string{"/a/.b"})
	if err != nil || len(access) != 1 || access[0] != "/a/.b" {
		t.Errorf("点开头段应当合法: %v, %v", access, err)
	}
	for _, raw := range []string{"", "目录A", "..", "/..", "/a/..", "/a\\b"} {
		if _, err := normalizeAccess([]string{raw}); err == nil {
			t.Errorf("normalizeAccess([%q]) 应当报错", raw)
		}
	}
	if _, err := normalizeAccess(nil); err == nil {
		t.Error("空 access 应当报错")
	}
}

// TestOpenRejectsInvalidAccess 验证可见范围是启动时的硬约束：
// 不存在、不是目录、是符号链接的条目都直接拒绝启动。
func TestOpenRejectsInvalidAccess(t *testing.T) {
	filesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(filesDir, "存在"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "普通文件"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filesDir, "存在"), filepath.Join(filesDir, "链接")); err != nil {
		t.Skipf("文件系统不支持符号链接: %v", err)
	}
	cases := map[string]string{
		"/不存在":  "not found",
		"/普通文件": "not a directory",
		"/链接":   "symbolic link",
		"/存在/子": "not found",
	}
	for access, want := range cases {
		cfg := Config{
			DataDir:           t.TempDir(),
			FilesDir:          filesDir,
			SessionSigningKey: "test_session_signing_key_0123456789",
			URLSignatureKey:   "test_url_signature_key_0123456789",
			Users: []User{{
				Name:        "tester",
				Password:    "tester_password_0123456789",
				Permissions: PermissionRead,
				Access:      []string{access},
			}},
		}
		_, err := Open(cfg)
		if err == nil {
			t.Errorf("access=%q 时 Open() 应当报错", access)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("access=%q 错误 = %v，想要包含 %q", access, err, want)
		}
	}
}

func TestOpenDownload(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "目录A/说明.md", "内容")
	download, err := instance.openDownload("目录A/说明.md")
	if err != nil {
		t.Fatalf("openDownload() 报错: %v", err)
	}
	defer download.Close()
	if download.Name != "说明.md" || download.Size != int64(len("内容")) {
		t.Errorf("下载元数据不对: %+v", download)
	}
	if _, err := instance.openDownload("不存在.txt"); !errors.Is(err, ErrNotFound) && !isBusiness(err, 404) {
		t.Errorf("不存在文件 = %v，想要 404", err)
	}
	if _, err := instance.openDownload("../etc/passwd"); !errors.Is(err, ErrInvalidPath) && !isBusiness(err, 400) {
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
	tester := userByName(t, instance, "tester")
	favorite, err := instance.ToggleFavorite(tester, "目录A/说明.md")
	if err != nil || !favorite {
		t.Fatalf("ToggleFavorite() = %v, %v，想要 true, nil", favorite, err)
	}
	// 重新打开实例，收藏应当还能读回来。
	reopened, err := openFavorites(instance.Config.DataDir, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.All()["目录A/说明.md"] {
		t.Errorf("收藏没有持久化: %+v", reopened.All())
	}
	favorite, err = instance.ToggleFavorite(tester, "目录A/说明.md")
	if err != nil || favorite {
		t.Fatalf("再次切换 = %v, %v，想要 false, nil", favorite, err)
	}
	if _, err := instance.ToggleFavorite(tester, "../bad"); err == nil {
		t.Fatal("非法路径应当报错")
	}
	info, err := os.Stat(filepath.Join(instance.Config.DataDir, favoritesDirName, "tester.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("tester.json 权限 = %o，想要 600", info.Mode().Perm())
	}
}

// TestFavoritesIsolatedPerUser 验证每个用户的收藏是独立文件：互不可见、互不覆盖。
func TestFavoritesIsolatedPerUser(t *testing.T) {
	instance := newTestApp(t)
	writeTestFile(t, instance, "目录A/说明.md", "doc")
	writeTestFile(t, instance, "目录B/map.zip", "map")
	tester := userByName(t, instance, "tester")
	viewer := userByName(t, instance, "viewer")

	if _, err := instance.ToggleFavorite(tester, "目录B/map.zip"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ToggleFavorite(viewer, "目录A/说明.md"); err != nil {
		t.Fatal(err)
	}
	viewerFiles, err := instance.ListFiles(viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(viewerFiles) != 1 || !viewerFiles[0].IsFavorite {
		t.Errorf("viewer 的收藏应只标记自己的文件: %+v", viewerFiles)
	}
	for _, name := range []string{"tester", "viewer"} {
		if _, err := os.Stat(filepath.Join(instance.Config.DataDir, favoritesDirName, name+".json")); err != nil {
			t.Errorf("用户 %q 的收藏文件不存在: %v", name, err)
		}
	}
	// 收藏写入要落在各自文件里，且不影响别人。
	raw, err := os.ReadFile(filepath.Join(instance.Config.DataDir, favoritesDirName, "tester.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "目录B/map.zip") {
		t.Errorf("tester.json 内容不对: %s", raw)
	}
	// 不可见路径不能收藏。
	if _, err := instance.ToggleFavorite(viewer, "目录B/map.zip"); !isBusiness(err, 404) {
		t.Errorf("收藏不可见路径 = %v，想要 404", err)
	}
}

func TestSignedURLRoundTrip(t *testing.T) {
	instance := newTestApp(t)
	tester := userByName(t, instance, "tester")
	writeTestFile(t, instance, "目录A/说明.md", "内容")
	link, err := instance.IssueShareLink(tester, "目录A/说明.md", core.SignedURLTTL)
	if err != nil {
		t.Fatalf("IssueShareLink() 报错: %v", err)
	}
	escapedPath, sig, exp, ok := splitSignedURL(t, link)
	if !ok {
		t.Fatalf("分享链接格式不对: %q", link)
	}
	download, err := instance.OpenSignedDownload("目录A/说明.md", "GET", escapedPath, sig, exp)
	if err != nil {
		t.Fatalf("OpenSignedDownload() 报错: %v", err)
	}
	download.Close()

	if _, err := instance.OpenSignedDownload("目录A/说明.md", "GET", escapedPath, "bad-signature", exp); !isBusiness(err, 403) {
		t.Errorf("坏签名 = %v，想要 403", err)
	}
	if _, err := instance.IssueShareLink(tester, "目录A/说明.md", 0); err != nil {
		t.Errorf("零 ttl 不应当在签发时报错: %v", err)
	}
}

// TestSignedURLUsesGlobalPath 验证签名 URL 覆盖的是用户看到的完整路径：
// 受限用户签 目录A/说明.md，链接就是 /s/目录A/说明.md，匿名下载直接打开。
func TestSignedURLUsesGlobalPath(t *testing.T) {
	instance := newTestApp(t)
	viewer := userByName(t, instance, "viewer")
	writeTestFile(t, instance, "目录A/说明.md", "内容")

	link, err := instance.IssueShareLink(viewer, "目录A/说明.md", core.SignedURLTTL)
	if err != nil {
		t.Fatalf("IssueShareLink(viewer) 报错: %v", err)
	}
	escapedPath, sig, exp, ok := splitSignedURL(t, link)
	if !ok {
		t.Fatalf("分享链接格式不对: %q", link)
	}
	wantPrefix := "/s/" + url.PathEscape("目录A") + "/"
	if !strings.HasPrefix(escapedPath, wantPrefix) {
		t.Errorf("链接路径 = %q，想要以 %q 开头（完整路径）", escapedPath, wantPrefix)
	}
	download, err := instance.OpenSignedDownload("目录A/说明.md", "GET", escapedPath, sig, exp)
	if err != nil {
		t.Fatalf("匿名下载应当能打开: %v", err)
	}
	download.Close()
}

func TestIssueSignedURLForMissingFile(t *testing.T) {
	instance := newTestApp(t)
	tester := userByName(t, instance, "tester")
	if _, err := instance.IssueDownloadURL(tester, "不存在.txt"); !isBusiness(err, 404) {
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
			Access:      []string{"/"},
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

// TestAccessIsCaseSensitive 验证可见范围按大小写精确匹配：
// 配置写成 /SOFTWARE 而实际目录是 /Software 时拒绝启动，不替用户猜大小写。
func TestAccessIsCaseSensitive(t *testing.T) {
	filesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(filesDir, "Software"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		DataDir:           t.TempDir(),
		FilesDir:          filesDir,
		SessionSigningKey: "test_session_signing_key_0123456789",
		URLSignatureKey:   "test_url_signature_key_0123456789",
		Users: []User{{
			Name: "case", Password: "case_password_0123456789",
			Permissions: PermissionRead, Access: []string{"/SOFTWARE"},
		}},
	}
	if _, err := Open(cfg); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("大小写不一致的 access 应当拒绝启动，得到 %v", err)
	}
	cfg.Users[0].Access = []string{"/Software"}
	instance, err := Open(cfg)
	if err != nil {
		t.Fatalf("大小写一致的 access 应当成功: %v", err)
	}
	t.Cleanup(func() { instance.Close() })
	user := userByName(t, instance, "case")
	writeTestFile(t, instance, "Software/说明.md", "x")
	if _, err := instance.IssueDownloadURL(user, "Software/说明.md"); err != nil {
		t.Errorf("真实大小写路径应当可签: %v", err)
	}
	// 请求路径大小写不一致：可见范围判定也是大小写敏感的。
	if _, err := instance.IssueDownloadURL(user, "software/说明.md"); !isBusiness(err, 404) {
		t.Errorf("大小写不一致的请求路径 = %v，想要 404", err)
	}
}
