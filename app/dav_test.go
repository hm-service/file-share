package app_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"file-share/core"
	"file-share/internal/testenv"
)

func TestDavPutGetAndDedupe(t *testing.T) {
	instance := testenv.New(t)
	content := []byte("dav 内容")
	if err := instance.DavMkdir("目录"); err != nil {
		t.Fatalf("DavMkdir() 报错: %v", err)
	}

	node, err := instance.DavPutFile("目录/a.txt", strings.NewReader(string(content)), int64(len(content)))
	if err != nil {
		t.Fatalf("DavPutFile() 报错: %v", err)
	}
	if node.Path != "目录/a.txt" || node.Parent != "目录" || node.Name != "a.txt" || node.IsDir {
		t.Fatalf("节点字段不对: %+v", node)
	}
	if node.SHA256 != testenv.SHA256(content) || node.Size != int64(len(content)) {
		t.Fatalf("节点内容字段不对: %+v", node)
	}

	download, err := instance.DavOpenFile("目录/a.txt")
	if err != nil {
		t.Fatalf("DavOpenFile() 报错: %v", err)
	}
	defer download.Close()
	got := make([]byte, node.Size)
	if _, err := download.Blob.Read(got); err != nil {
		t.Fatalf("读 blob 失败: %v", err)
	}
	if string(got) != string(content) || download.Name != "a.txt" {
		t.Fatalf("下载内容 = %q name=%q", got, download.Name)
	}

	// 同一个内容走 file 表再上传一次：物理文件只存一份。
	record := testenv.SeedUpload(t, instance, "same.txt", content)
	if record.SHA256 != node.SHA256 {
		t.Fatalf("两条记录的 sha 不一致: %s vs %s", record.SHA256, node.SHA256)
	}
	if remaining, err := instance.Store.CountBySHA(node.SHA256); err != nil || remaining != 2 {
		t.Fatalf("跨表引用计数 = %d, %v，想要 2", remaining, err)
	}

	// 删掉 dav 记录：file 记录还在，blob 不能删。
	if err := instance.DavDelete("目录/a.txt"); err != nil {
		t.Fatalf("DavDelete() 报错: %v", err)
	}
	if _, err := os.Stat(testenv.BlobPath(instance, node.SHA256)); err != nil {
		t.Fatalf("file 记录仍引用时 blob 不应被删: %v", err)
	}
	// 再删 file 记录：两边都没有引用，blob 才落盘消失。
	if err := instance.DeleteFile(record.ID); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if _, err := os.Stat(testenv.BlobPath(instance, node.SHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("两边都不引用后 blob 应当删除，得到 %v", err)
	}
}

func TestDavMkdirMoveAndDeleteSubtree(t *testing.T) {
	instance := testenv.New(t)
	for _, dir := range []string{"a", "a/b", "x"} {
		if err := instance.DavMkdir(dir); err != nil {
			t.Fatalf("DavMkdir(%q) 报错: %v", dir, err)
		}
	}
	if _, err := instance.DavPutFile("a/b/c.txt", strings.NewReader("c"), 1); err != nil {
		t.Fatalf("DavPutFile() 报错: %v", err)
	}

	// 目录整体改名：子节点前缀跟着换。
	if err := instance.DavMove("a", "x/a2"); err != nil {
		t.Fatalf("DavMove() 报错: %v", err)
	}
	if _, err := instance.DavStat("x/a2/b/c.txt"); err != nil {
		t.Fatalf("移动后的子节点找不到: %v", err)
	}
	if _, err := instance.DavStat("a"); !isBusinessError(err, 404) {
		t.Fatalf("旧路径应当 404，得到 %v", err)
	}

	// 目录不能移进自己的子树。
	if err := instance.DavMove("x/a2", "x/a2/b/inner"); !isBusinessError(err, 400) {
		t.Fatalf("移进自己的子树应当 400，得到 %v", err)
	}

	// 递归删除：子节点一起消失。
	if err := instance.DavDelete("x/a2"); err != nil {
		t.Fatalf("DavDelete() 报错: %v", err)
	}
	if _, err := instance.DavStat("x/a2/b/c.txt"); !isBusinessError(err, 404) {
		t.Fatalf("删除后子节点应当 404，得到 %v", err)
	}
	entries, err := instance.DavList("x")
	if err != nil || len(entries) != 0 {
		t.Fatalf("x 下仍有 %d 个节点，%v", len(entries), err)
	}
}

func TestDavValidationAndConflicts(t *testing.T) {
	instance := testenv.New(t)
	if _, err := instance.DavPutFile("missing/a.txt", strings.NewReader("x"), 1); !isBusinessError(err, 404) {
		t.Fatalf("父目录不存在应当 404，得到 %v", err)
	}
	if _, err := instance.DavPutFile("", strings.NewReader("x"), 1); !isBusinessError(err, 400) {
		t.Fatalf("空路径应当 400，得到 %v", err)
	}
	if _, err := instance.DavPutFile("a.txt", strings.NewReader("x"), 1+1<<40); !isBusinessError(err, 413) {
		t.Fatalf("超过上限应当 413，得到 %v", err)
	}

	if err := instance.DavMkdir("dir"); err != nil {
		t.Fatalf("DavMkdir() 报错: %v", err)
	}
	if err := instance.DavMkdir("dir"); !isBusinessError(err, 409) {
		t.Fatalf("重复建目录应当 409，得到 %v", err)
	}
	if _, err := instance.DavPutFile("dir", strings.NewReader("x"), 1); !isBusinessError(err, 409) {
		t.Fatalf("PUT 覆盖目录应当 409，得到 %v", err)
	}
	if _, err := instance.DavPutFile("dir/x.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("DavPutFile() 报错: %v", err)
	}
	if _, err := instance.DavOpenFile("dir"); !isBusinessError(err, 404) {
		t.Fatalf("把目录当文件打开应当 404，得到 %v", err)
	}
	if _, err := instance.DavOpenFile("missing.txt"); !isBusinessError(err, 404) {
		t.Fatalf("不存在的文件应当 404，得到 %v", err)
	}
	if err := instance.DavDelete(""); !isBusinessError(err, 403) {
		t.Fatalf("删除根应当 403，得到 %v", err)
	}
}

func TestDavOverwriteKeepsOneRecord(t *testing.T) {
	instance := testenv.New(t)
	first := []byte("first")
	second := []byte("second")

	if _, err := instance.DavPutFile("a.txt", strings.NewReader(string(first)), int64(len(first))); err != nil {
		t.Fatalf("DavPutFile() 报错: %v", err)
	}
	node, err := instance.DavPutFile("a.txt", strings.NewReader(string(second)), int64(len(second)))
	if err != nil {
		t.Fatalf("覆盖写入报错: %v", err)
	}
	if node.SHA256 != testenv.SHA256(second) {
		t.Fatalf("覆盖后 sha = %s，想要新内容", node.SHA256)
	}
	if _, err := os.Stat(testenv.BlobPath(instance, testenv.SHA256(first))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧 blob 无引用后应当删除，得到 %v", err)
	}
	entries, err := instance.DavList("")
	if err != nil || len(entries) != 1 {
		t.Fatalf("覆盖后节点数 = %d, %v，想要 1", len(entries), err)
	}
}

func isBusinessError(err error, status int) bool {
	var business *core.Error
	if !errors.As(err, &business) {
		return false
	}
	return business.Status == status
}
