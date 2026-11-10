package db

import (
	"errors"
	"testing"

	"file-share/core"
)

// groupPaths 把分组行拆成路径列表，断言顺序时只关心路径。
func groupPaths(groups []Group) []string {
	paths := make([]string, 0, len(groups))
	for _, group := range groups {
		paths = append(paths, group.Path)
	}
	return paths
}

// groupsByParent 把分组行按父路径聚合：跨父级的分组在数组里的相对顺序不在契约内，
// 契约只保证「同一父级内按 sort_order」。
func groupsByParent(groups []Group) map[string][]string {
	byParent := make(map[string][]string)
	for _, group := range groups {
		byParent[group.Parent] = append(byParent[group.Parent], group.Path)
	}
	return byParent
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
// group_path 默认为空。
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
		if file.GroupPath != "" {
			t.Errorf("%s group_path = %q，想要空串", file.ID, file.GroupPath)
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
	if updated.Filename != "新名字.txt" || updated.SortOrder != 2 || updated.GroupPath != "" {
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
	if updated.ID != "aa" || updated.GroupPath != "工作" || updated.SortOrder != 4 {
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
	if updated.Filename != "重命名.txt" || updated.GroupPath != "归档" || updated.SortOrder != 5 {
		t.Fatalf("同时改名与分组后 = %+v，想要新名字、分组「归档」、sort_order 5", updated)
	}

	// 清空分组 = 未分组，同样移到末尾。
	updated, err = store.UpdateFile("aa", nil, textPtr(""))
	if err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if updated.GroupPath != "" || updated.SortOrder != 6 {
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
		file.GroupPath = item.group
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
		if file.GroupPath != "新组" && file.GroupPath != "保留组" {
			t.Errorf("%s 的分组 = %q，想要新组或保留组", file.ID, file.GroupPath)
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

// 分组顺序跟随分组生命周期：新分组排到末尾，改名保持位置，搬空 / 删光文件后分组仍保留。
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
	if groups, err := store.Groups(); err != nil || !samePaths(groupPaths(groups), "一组", "二组") {
		t.Fatalf("Groups() = %v, %v，想要 [一组 二组]", groupPaths(groups), err)
	}

	// 拖拽后的顺序落库。
	if err := store.SetGroupOrder("", []string{"二组", "一组"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || !samePaths(groupPaths(groups), "二组", "一组") {
		t.Fatalf("重排后 Groups() = %v, %v，想要 [二组 一组]", groupPaths(groups), err)
	}

	// 提交的分组集合必须与现状完全一致。
	for name, names := range map[string][]string{
		"少一个": {"二组"},
		"多一个": {"二组", "一组", "三组"},
		"重复":  {"二组", "二组"},
		"空列表": {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.SetGroupOrder("", names); !errors.Is(err, ErrGroupOrderMismatch) {
				t.Errorf("SetGroupOrder(%v) = %v，想要 ErrGroupOrderMismatch", names, err)
			}
		})
	}

	// 改名保持顺序位置。
	if _, err := store.RenameGroup("二组", "新二组"); err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || !samePaths(groupPaths(groups), "新二组", "一组") {
		t.Fatalf("改名后 Groups() = %v, %v，想要 [新二组 一组]", groupPaths(groups), err)
	}

	// 搬走最后一个文件：分组保留，顺序不变（空分组是合法状态）。
	if _, err := store.UpdateFile("aa", nil, textPtr("新二组")); err != nil {
		t.Fatalf("UpdateFile() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || !samePaths(groupPaths(groups), "新二组", "一组") {
		t.Fatalf("搬走最后文件后 Groups() = %v, %v，想要 [新二组 一组]", groupPaths(groups), err)
	}

	// 文件全部删光，分组仍然保留。
	if err := store.DeleteFile("aa"); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if err := store.DeleteFile("bb"); err != nil {
		t.Fatalf("DeleteFile() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || len(groups) != 2 {
		t.Fatalf("删空文件后 Groups() = %v, %v，想要两个空分组", groupPaths(groups), err)
	}

	// 手动新建空分组：追加到末尾；同名冲突。
	created, err := store.CreateGroup("空组")
	if err != nil || created.Path != "空组" || created.Parent != "" || created.Name != "空组" || created.SortOrder != 2 {
		t.Fatalf("CreateGroup() = %+v, %v，想要顶级空组、sort_order 2", created, err)
	}
	if groups, err := store.Groups(); err != nil || !samePaths(groupPaths(groups), "新二组", "一组", "空组") {
		t.Fatalf("新建后 Groups() = %v, %v，想要 [新二组 一组 空组]", groupPaths(groups), err)
	}
	if _, err := store.CreateGroup("空组"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("重复新建应当返回 ErrGroupExists，得到 %v", err)
	}

	// 空分组也参与排序与改名，位置保持。
	if err := store.SetGroupOrder("", []string{"空组", "一组", "新二组"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}
	if _, err := store.RenameGroup("空组", "改名空组"); err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	if groups, err := store.Groups(); err != nil || !samePaths(groupPaths(groups), "改名空组", "一组", "新二组") {
		t.Fatalf("改名空分组后 Groups() = %v, %v，想要 [改名空组 一组 新二组]", groupPaths(groups), err)
	}
	if _, err := store.RenameGroup("改名空组", "一组"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("改成已存在的分组应当返回 ErrGroupExists，得到 %v", err)
	}
	if _, err := store.RenameGroup("不存在", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("改不存在的分组应当返回 ErrNotFound，得到 %v", err)
	}
}

// 两级分组：子分组挂在父分组下、同级独立排序；父分组改名时子分组与文件路径一起改。
func TestNestedGroups(t *testing.T) {
	store := testStore(t)
	for _, item := range []struct {
		id   string
		path string
	}{
		{"aa", "工作"},
		{"bb", "工作/项目A"},
		{"cc", "工作/项目B"},
		{"dd", "生活"},
	} {
		file := sampleFile(item.id, "sha-"+item.id, 100)
		file.GroupPath = item.path
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	// 顶层按创建顺序，子分组只出现在自己的父级下。
	groups, err := store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	byParent := groupsByParent(groups)
	if !samePaths(byParent[""], "工作", "生活") {
		t.Fatalf("顶层分组 = %v，想要 [工作 生活]", byParent[""])
	}
	if !samePaths(byParent["工作"], "工作/项目A", "工作/项目B") {
		t.Fatalf("工作下的子分组 = %v，想要 [工作/项目A 工作/项目B]", byParent["工作"])
	}
	for _, group := range groups {
		switch group.Path {
		case "工作":
			if group.Parent != "" || group.Name != "工作" {
				t.Errorf("顶层分组 = %+v，想要 parent 为空、name 工作", group)
			}
		case "工作/项目A":
			if group.Parent != "工作" || group.Name != "项目A" {
				t.Errorf("子分组 = %+v，想要 parent 工作、name 项目A", group)
			}
		}
	}

	// 新建子分组追加到同一父级的末尾，父级不存在时一并创建。
	created, err := store.CreateGroup("工作/项目C")
	if err != nil {
		t.Fatalf("CreateGroup() 报错: %v", err)
	}
	if created.Parent != "工作" || created.Name != "项目C" || created.SortOrder != 2 {
		t.Errorf("CreateGroup() = %+v，想要 parent 工作、name 项目C、sort_order 2", created)
	}
	if _, err := store.CreateGroup("全新/子"); err != nil {
		t.Fatalf("CreateGroup() 补父级报错: %v", err)
	}
	groups, err = store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	byParent = groupsByParent(groups)
	if !samePaths(byParent[""], "工作", "生活", "全新") {
		t.Fatalf("顶层分组 = %v，想要 [工作 生活 全新]", byParent[""])
	}
	if !samePaths(byParent["工作"], "工作/项目A", "工作/项目B", "工作/项目C") {
		t.Fatalf("工作下的子分组 = %v", byParent["工作"])
	}
	if !samePaths(byParent["全新"], "全新/子") {
		t.Fatalf("全新下的子分组 = %v", byParent["全新"])
	}

	// 同级重排只动这个父级下的顺序；顶层重排不影响子分组。
	if err := store.SetGroupOrder("工作", []string{"项目C", "项目B", "项目A"}); err != nil {
		t.Fatalf("SetGroupOrder() 报错: %v", err)
	}
	groups, err = store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	if !samePaths(groupsByParent(groups)["工作"], "工作/项目C", "工作/项目B", "工作/项目A") {
		t.Fatalf("重排子分组后 Groups() = %v", groupPaths(groups))
	}

	// 某个父级下的集合必须完全一致：拿顶层名字来排子级应当对不上。
	if err := store.SetGroupOrder("工作", []string{"项目A", "项目B"}); !errors.Is(err, ErrGroupOrderMismatch) {
		t.Errorf("少一个子分组应当返回 ErrGroupOrderMismatch，得到 %v", err)
	}
	if err := store.SetGroupOrder("不存在", []string{}); err != nil {
		t.Errorf("空父级下没有分组时，空顺序应当成功，得到 %v", err)
	}

	// 父分组改名：子分组的路径与父路径、组内文件的分组路径一起改。
	changed, err := store.RenameGroup("工作", "工作区")
	if err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	if changed != 3 {
		t.Errorf("重命名影响文件数 = %d，想要 3", changed)
	}
	groups, err = store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	byParent = groupsByParent(groups)
	if !samePaths(byParent[""], "工作区", "生活", "全新") {
		t.Fatalf("父分组改名后顶层 = %v", byParent[""])
	}
	if !samePaths(byParent["工作区"], "工作区/项目C", "工作区/项目B", "工作区/项目A") {
		t.Fatalf("父分组改名后子分组 = %v", byParent["工作区"])
	}
	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	paths := make(map[string]string, len(files))
	for _, file := range files {
		paths[file.ID] = file.GroupPath
	}
	for id, want := range map[string]string{
		"aa": "工作区",
		"bb": "工作区/项目A",
		"cc": "工作区/项目B",
	} {
		if paths[id] != want {
			t.Errorf("%s 的 group_path = %q，想要 %q", id, paths[id], want)
		}
	}

	// 子分组改名只改自己那一段，父路径不变。
	if _, err := store.RenameGroup("工作区/项目A", "项目X"); err != nil {
		t.Fatalf("RenameGroup() 报错: %v", err)
	}
	groups, err = store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	found := false
	for _, group := range groups {
		if group.Path == "工作区/项目X" {
			found = true
			if group.Parent != "工作区" || group.Name != "项目X" {
				t.Errorf("子分组改名后 = %+v", group)
			}
		}
	}
	if !found {
		t.Fatalf("子分组改名后 Groups() = %v，找不到 工作区/项目X", groupPaths(groups))
	}
}

// 删除分组：子分组一并删除；文件要么一起删掉（返回 sha），要么上移到被删分组的父路径。
func TestDeleteGroup(t *testing.T) {
	store := testStore(t)
	for _, item := range []struct {
		id   string
		path string
	}{
		{"aa", "工作"},
		{"bb", "工作/项目A"},
		{"cc", "工作/项目B"},
		{"dd", "生活"},
	} {
		file := sampleFile(item.id, "sha-"+item.id, 100)
		file.GroupPath = item.path
		if err := store.InsertFile(file); err != nil {
			t.Fatalf("InsertFile() 报错: %v", err)
		}
	}

	// 删不存在的分组：ErrNotFound。
	if _, err := store.DeleteGroup("不存在", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("删不存在的分组应当返回 ErrNotFound，得到 %v", err)
	}

	// 删除子分组且保留文件：文件回到上级分组。
	if removed, err := store.DeleteGroup("工作/项目A", false); err != nil || len(removed) != 0 {
		t.Fatalf("DeleteGroup() = (%v, %v)，想要删分组但不删文件", removed, err)
	}
	if groups := groupsByParent(mustGroups(t, store)); !samePaths(groups["工作"], "工作/项目B") {
		t.Fatalf("删除子分组后 工作 下 = %v，想要只剩 工作/项目B", groups["工作"])
	}
	if got, err := store.File("bb"); err != nil || got.GroupPath != "工作" {
		t.Fatalf("bb 的 group_path = %+v, %v，想要上移到 工作", got, err)
	}

	// 删除顶级分组且保留文件：子分组一并删除，文件回到未分组。
	if removed, err := store.DeleteGroup("工作", false); err != nil || len(removed) != 0 {
		t.Fatalf("DeleteGroup() = (%v, %v)", removed, err)
	}
	groups := groupsByParent(mustGroups(t, store))
	if _, ok := groups["工作"]; ok {
		t.Fatalf("顶级分组删除后 groups = %v，不该再有 工作", groups)
	}
	for _, id := range []string{"aa", "bb", "cc"} {
		if got, err := store.File(id); err != nil || got.GroupPath != "" {
			t.Errorf("%s 的 group_path = %q, %v，想要空串", id, got.GroupPath, err)
		}
	}

	// 删除分组且删除文件：返回被删文件的 sha，记录消失。
	removed, err := store.DeleteGroup("生活", true)
	if err != nil {
		t.Fatalf("DeleteGroup() 报错: %v", err)
	}
	if len(removed) != 1 || removed[0] != "sha-dd" {
		t.Fatalf("removed = %v，想要 [sha-dd]", removed)
	}
	if _, err := store.File("dd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("dd 应当已被删除，得到 %v", err)
	}
	if groups := mustGroups(t, store); len(groups) != 0 {
		t.Errorf("分组应当清空，得到 %v", groupPaths(groups))
	}
}

// mustGroups 是测试里的取值捷径。
func mustGroups(t *testing.T, store *Store) []Group {
	t.Helper()
	groups, err := store.Groups()
	if err != nil {
		t.Fatalf("Groups() 报错: %v", err)
	}
	return groups
}
