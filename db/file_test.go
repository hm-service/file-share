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

// 改分组会把记录移到末尾；只改名保持原顺序；两个字段可以一起改。
func TestUpdateFile(t *testing.T) {
	store := testStore(t)
	for _, id := range []string{"aa", "bb", "cc"} {
		if err := store.InsertFile(sampleFile(id, "sha-"+id, 100)); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	// 只改名：文件名变了，顺序不动。
	updated, err := store.UpdateFile("bb", textPtr("新名字.txt"), nil)
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.Filename != "新名字.txt" || updated.SortOrder != 2 || updated.GroupName != "" {
		t.Fatalf("只改名后 = %+v，想要新文件名且 sort_order 2", updated)
	}
	if files, err := store.ListFiles(); err != nil || len(files) != 3 || files[1].ID != "bb" {
		t.Fatalf("只改名后列表 = %+v, %v，想要 bb 仍在第二位", files, err)
	}

	// 改分组：移到末尾。
	updated, err = store.UpdateFile("aa", nil, textPtr("工作"))
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.ID != "aa" || updated.GroupName != "工作" || updated.SortOrder != 4 {
		t.Fatalf("UpdateFile() = %+v，想要分组「工作」且 sort_order 4", updated)
	}
	if files, err := store.ListFiles(); err != nil || len(files) != 3 || files[2].ID != "aa" {
		t.Fatalf("改分组后列表 = %+v, %v，想要 aa 在最后", files, err)
	}

	// 两个字段一起改：同样移到末尾。
	updated, err = store.UpdateFile("cc", textPtr("重命名.txt"), textPtr("归档"))
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.Filename != "重命名.txt" || updated.GroupName != "归档" || updated.SortOrder != 5 {
		t.Fatalf("同时改名与分组后 = %+v，想要新名字、分组「归档」、sort_order 5", updated)
	}

	// 清空分组 = 未分组，同样移到末尾。
	updated, err = store.UpdateFile("aa", nil, textPtr(""))
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.GroupName != "" || updated.SortOrder != 6 {
		t.Fatalf("清空分组后 = %+v，想要空串分组与 sort_order 6", updated)
	}

	if err := store.InsertFile(sampleFile("dd", "sha-dd", 100)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}
	if got, err := store.File("dd"); err != nil || got.SortOrder != 7 {
		t.Fatalf("新插入记录的 sort_order = %+v, %v，想要 7", got, err)
	}

	if _, err := store.UpdateFile("absent", nil, textPtr("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的记录应当返回 ErrNotFound，得到 %v", err)
	}
	if _, err := store.UpdateFile("aa", nil, nil); err == nil {
		t.Error("两个字段都为 nil 应当报错")
	}
}

// textPtr 让测试里能直接写「改这个字段」；nil 表示不动。
func textPtr(value string) *string { return &value }

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

// 重命名分组批量改写组内记录，保持各自 sort_order；目标分组已存在时拒绝合并且不改任何记录。
func TestRenameGroup(t *testing.T) {
	store := testStore(t)
	for _, item := range []struct {
		id    string
		group string
	}{
		{"aa", "旧组"},
		{"bb", "旧组"},
		{"cc", "保留组"},
	} {
		file := sampleFile(item.id, "sha-"+item.id, 100)
		file.GroupName = item.group
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}
	// 组内第二位的记录单独排到最前，验证重命名不动 sort_order。
	if err := store.SetOrder([]core.OrderItem{{ID: "bb", SortOrder: 0}}); err != nil {
		t.Fatalf("SetOrder() 报错: %v", err)
	}

	changed, err := store.RenameGroup("旧组", "新组")
	if err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	if changed != 2 {
		t.Errorf("重命名条数 = %d，想要 2", changed)
	}
	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	for _, file := range files {
		if file.GroupName != "新组" && file.GroupName != "保留组" {
			t.Errorf("%s 的分组 = %q，想要新组或保留组", file.ID, file.GroupName)
		}
	}
	if files[0].ID != "bb" || files[1].ID != "aa" || files[2].ID != "cc" {
		t.Errorf("重命名后列表顺序 = [%s %s %s]，想要 [bb aa cc]", files[0].ID, files[1].ID, files[2].ID)
	}

	if _, err := store.RenameGroup("新组", "保留组"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("目标分组已存在应当返回 ErrGroupExists，得到 %v", err)
	}
	if _, err := store.RenameGroup("不存在", "任意"); !errors.Is(err, ErrNotFound) {
		t.Errorf("源分组不存在应当返回 ErrNotFound，得到 %v", err)
	}
	// 改成同名是幂等的：仍然算重命名成功，不改动顺序。
	if changed, err := store.RenameGroup("新组", "新组"); err != nil || changed != 2 {
		t.Errorf("同名重命名 = (%d, %v)，想要 2 条成功", changed, err)
	}
}

// 分组顺序跟随分组生命周期：新分组排到末尾，改名保持位置，最后一个文件搬走 / 删掉时清理顺序行。
func TestGroupOrderLifecycle(t *testing.T) {
	store := testStore(t)
	for _, id := range []string{"aa", "bb"} {
		if err := store.InsertFile(sampleFile(id, "sha-"+id, 100)); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	// 新分组按创建顺序追加到末尾。
	if _, err := store.UpdateFile("aa", nil, textPtr("一组")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if _, err := store.UpdateFile("bb", nil, textPtr("二组")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || len(groups) != 2 || groups[0] != "一组" || groups[1] != "二组" {
		t.Fatalf("Groups() = %v, %v，想要 [一组 二组]", groups, err)
	}

	// 拖拽后的顺序落库。
	if err := store.SetGroupOrder([]string{"二组", "一组"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || groups[0] != "二组" || groups[1] != "一组" {
		t.Fatalf("重排后 Groups() = %v, %v，想要 [二组 一组]", groups, err)
	}

	// 提交的分组集合必须与现状完全一致。
	for name, names := range map[string][]string{
		"少一个": {"二组"},
		"多一个": {"二组", "一组", "三组"},
		"重复":  {"二组", "二组"},
		"空列表": {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.SetGroupOrder(names); !errors.Is(err, ErrGroupOrderMismatch) {
				t.Errorf("SetGroupOrder(%v) = %v，想要 ErrGroupOrderMismatch", names, err)
			}
		})
	}

	// 改名保持顺序位置。
	if _, err := store.RenameGroup("二组", "新二组"); err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || groups[0] != "新二组" || groups[1] != "一组" {
		t.Fatalf("改名后 Groups() = %v, %v，想要 [新二组 一组]", groups, err)
	}

	// 一组最后一个文件搬去别的组：一组消失，顺序行被清理。
	if _, err := store.UpdateFile("aa", nil, textPtr("新二组")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || len(groups) != 1 || groups[0] != "新二组" {
		t.Fatalf("搬走最后文件后 Groups() = %v, %v，想要 [新二组]", groups, err)
	}

	// 删掉组内最后一个文件：分组与顺序行一起消失。
	if err := store.DeleteFile("aa"); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if err := store.DeleteFile("bb"); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || len(groups) != 0 {
		t.Fatalf("删空后 Groups() = %v, %v，想要空", groups, err)
	}
	var rows int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM group_order").Scan(&rows); err != nil {
		t.Fatalf("查询 group_order 失败: %v", err)
	}
	if rows != 0 {
		t.Errorf("删空后 group_order 还有 %d 行", rows)
	}
}
