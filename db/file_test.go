package db

import (
	"errors"
	"testing"
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

func TestInsertAndListFiles(t *testing.T) {
	store := testStore(t)

	// 先插一条旧的，再插两条同一秒的：同一秒内按 id 升序，保证排序稳定。
	if err := store.InsertFile(sampleFile("cccc", "sha-old", 100)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}
	if err := store.InsertFile(sampleFile("bbbb", "sha-new", 200)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}
	if err := store.InsertFile(sampleFile("aaaa", "sha-new", 200)); err != nil {
		t.Fatalf("InsertFile() 报错: %v", err)
	}

	files, err := store.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles() 报错: %v", err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.ID)
	}
	want := []string{"aaaa", "bbbb", "cccc"}
	if len(got) != len(want) {
		t.Fatalf("列表 = %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("列表 = %v，想要 %v", got, want)
		}
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
