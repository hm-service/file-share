package db

import (
	"errors"
	"testing"

	"file-share/core"
)

func sampleFile(id, sha string, createdAt int64) File {
	return File{
		ID:        id,
		Filename:  id + ".txt",
		SHA256:    sha,
		Size:      int64(len(id)),
		CreatedAt: createdAt,
	}
}

// 插入顺序就是列表顺序：每次插入都排在末尾（sort_order = 当前最大值 + 1），
// group_name 默认为空。
func TestInsertAndListFiles(t *testing.T) {
	store := testStore(t)
	for _, file := range []File{
		sampleFile("cccc", "sha-old", 100),
		sampleFile("bbbb", "sha-new", 200),
		sampleFile("aaaa", "sha-new", 200),
	} {
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.ID)
	}
	want := []string{"cccc", "bbbb", "aaaa"}
	if len(got) != len(want) {
		t.Fatalf("列表 = %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("列表 = %v，想要 %v", got, want)
		}
	}
	for i, file := range files {
		if file.SortOrder != int64(i+1) {
			t.Errorf("%s sort_order = %d，想要 %d", file.ID, file.SortOrder, i+1)
		}
		if file.GroupName != "" {
			t.Errorf("%s group_name = %q，想要空串", file.ID, file.GroupName)
		}
	}
}

// sort_order 相同时按创建时间倒序、id 升序；整体按 sort_order 升序。
func TestListOrdersBySortOrder(t *testing.T) {
	store := testStore(t)
	for _, file := range []File{
		sampleFile("aa", "sha-aa", 100),
		sampleFile("bb", "sha-bb", 200),
		sampleFile("cc", "sha-cc", 200),
		sampleFile("dd", "sha-dd", 300),
	} {
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}
	// dd 单独一档；bb / cc / aa 同档：先 created_at 倒序（bb/cc 200 在 aa 100 前），
	// 同一秒的再按 id 升序（bb < cc）。
	if err := store.SetOrder([]core.OrderItem{
		{ID: "dd", SortOrder: 1},
		{ID: "bb", SortOrder: 2},
		{ID: "cc", SortOrder: 2},
		{ID: "aa", SortOrder: 2},
	}); err != nil {
		t.Fatalf("SetOrder() 报错: %v", err)
	}

	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.ID)
	}
	want := []string{"dd", "bb", "cc", "aa"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("列表 = %v，想要 %v", got, want)
		}
	}
}

// 更新分组会把记录移到末尾，之后新插入的记录仍然从新的最大值继续 +1。
func TestUpdateGroupMovesToEnd(t *testing.T) {
	store := testStore(t)
	for _, id := range []string{"aa", "bb", "cc"} {
		if err := store.InsertFile(sampleFile(id, "sha-"+id, 100)); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	updated, err := store.UpdateGroup("aa", "工作")
	if err != nil {
		t.Fatalf("UpdateGroup() 报错: %v", err)
	}
	if updated.ID != "aa" || updated.GroupName != "工作" || updated.SortOrder != 4 {
		t.Fatalf("UpdateGroup() = %+v，想要分组「工作」且 sort_order 4", updated)
	}
	if files, err := store.ListFiles(); err != nil || len(files) != 3 || files[2].ID != "aa" {
		t.Fatalf("改分组后列表 = %+v, %v，想要 aa 在最后", files, err)
	}

	// 清空分组 = 未分组，同样移到末尾。
	updated, err = store.UpdateGroup("aa", "")
	if err != nil {
		t.Fatalf("UpdateGroup() 报错: %v", err)
	}
	if updated.GroupName != "" || updated.SortOrder != 5 {
		t.Fatalf("清空分组后 = %+v，想要空串分组与 sort_order 5", updated)
	}

	if err := store.InsertFile(sampleFile("dd", "sha-dd", 100)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}
	if got, err := store.File("dd"); err != nil || got.SortOrder != 6 {
		t.Fatalf("新插入记录的 sort_order = %+v, %v，想要 6", got, err)
	}

	if _, err := store.UpdateGroup("absent", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
}

// 批量排序整体生效；有一条记录不存在就整体回滚。
func TestSetOrder(t *testing.T) {
	store := testStore(t)
	for _, id := range []string{"aa", "bb", "cc"} {
		if err := store.InsertFile(sampleFile(id, "sha-"+id, 100)); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	if err := store.SetOrder([]core.OrderItem{
		{ID: "cc", SortOrder: 10},
		{ID: "aa", SortOrder: 20},
		{ID: "bb", SortOrder: 30},
	}); err != nil {
		t.Fatalf("SetOrder() 报错: %v", err)
	}
	want := []string{"cc", "aa", "bb"}
	gotIDs := func() []string {
		files, err := store.ListFiles()
		if err != nil {
			t.Fatalf("ListFiles() 报错: %v", err)
		}
		ids := make([]string, 0, len(files))
		for _, file := range files {
			ids = append(ids, file.ID)
		}
		return ids
	}
	if got := gotIDs(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("列表 = %v，想要 %v", got, want)
	}

	// 不存在的 id：整批回滚，原顺序不变。
	if err := store.SetOrder([]core.OrderItem{
		{ID: "aa", SortOrder: 1},
		{ID: "absent", SortOrder: 2},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
	if got := gotIDs(); got[0] != "cc" || got[1] != "aa" || got[2] != "bb" {
		t.Fatalf("回滚后列表 = %v，想要原顺序 %v", got, want)
	}
}

func TestListFilesEmptyIsEmptySlice(t *testing.T) {
	store := testStore(t)
	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	if files == nil {
		t.Error("空列表应当是空切片而不是 nil（JSON 要序列化成 []）")
	}
	if len(files) != 0 {
		t.Errorf("列表长度 = %d，想要 0", len(files))
	}
}

func TestFile(t *testing.T) {
	store := testStore(t)
	record := sampleFile("id-one", "sha", 123)
	record.IsFavorite = true
	record.IsPublic = true
	if err := store.InsertFile(record); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}

	got, err := store.File("id-one")
	if err != nil {
		t.Fatalf("File() 报错: %v", err)
	}
	// InsertFile 会把记录追加到末尾，所以库里拿到的 sort_order 是 1 而不是传进去的 0。
	record.SortOrder = 1
	if *got != record {
		t.Errorf("File() = %+v，想要 %+v", *got, record)
	}

	if _, err := store.File("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
}

func TestPublicFileHidesPrivateRecords(t *testing.T) {
	store := testStore(t)
	private := sampleFile("private", "sha1", 1)
	public := sampleFile("public", "sha2", 2)
	public.IsPublic = true
	for _, file := range []File{private, public} {
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	if got, err := store.PublicFile("public"); err != nil || got.ID != "public" {
		t.Fatalf("PublicFile(公开) = %v, %v", got, err)
	}
	if _, err := store.PublicFile("private"); !errors.Is(err, ErrNotFound) {
		t.Errorf("私有记录应当返回 ErrNotFound，得到 %v", err)
	}
	if _, err := store.PublicFile("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
}

func TestToggleFavorite(t *testing.T) {
	store := testStore(t)
	if err := store.InsertFile(sampleFile("id-one", "sha", 1)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}

	for i, want := range []bool{true, false, true} {
		got, err := store.ToggleFavorite("id-one")
		if err != nil {
			t.Fatalf("第 %d 次 ToggleFavorite() 报错: %v", i, err)
		}
		if got != want {
			t.Errorf("第 %d 次切换后 = %v，想要 %v", i, got, want)
		}
	}

	if _, err := store.ToggleFavorite("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
}

func TestTogglePublic(t *testing.T) {
	store := testStore(t)
	if err := store.InsertFile(sampleFile("id-one", "sha", 1)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}
	got, err := store.TogglePublic("id-one")
	if err != nil {
		t.Fatalf("TogglePublic() 报错: %v", err)
	}
	if !got {
		t.Error("第一次切换后应当是公开")
	}
	if _, err := store.TogglePublic("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
}

func TestDeleteFileAndCountBySHA(t *testing.T) {
	store := testStore(t)
	sha := "same-content"
	first := sampleFile("id-one", sha, 1)
	second := sampleFile("id-two", sha, 2)
	other := sampleFile("id-three", "other-content", 3)
	for _, file := range []File{first, second, other} {
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	if count, err := store.CountBySHA(sha); err != nil || count != 2 {
		t.Fatalf("CountBySHA() = %d, %v，想要 2", count, err)
	}
	if err := store.DeleteFile("id-one"); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if count, err := store.CountBySHA(sha); err != nil || count != 1 {
		t.Fatalf("删一条后 CountBySHA() = %d, %v，想要 1", count, err)
	}
	if err := store.DeleteFile("id-two"); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if count, err := store.CountBySHA(sha); err != nil || count != 0 {
		t.Fatalf("删两条后 CountBySHA() = %d, %v，想要 0", count, err)
	}
	// 另一个 sha 不受影响。
	if count, err := store.CountBySHA("other-content"); err != nil || count != 1 {
		t.Fatalf("其它 sha 的 CountBySHA() = %d, %v，想要 1", count, err)
	}
	if err := store.DeleteFile("id-one"); !errors.Is(err, ErrNotFound) {
		t.Errorf("再删一次应当返回 ErrNotFound，得到 %v", err)
	}
}
