package core

import "testing"

func TestUUID4Shape(t *testing.T) {
	seen := make(map[string]bool, 128)
	for i := 0; i < 128; i++ {
		id, err := UUID4()
		if err != nil {
			t.Fatalf("UUID4() 报错: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("长度 = %d，想要 32: %q", len(id), id)
		}
		for _, r := range id {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				t.Fatalf("出现非小写十六进制字符 %q: %q", r, id)
			}
		}
		if id[12] != '4' {
			t.Errorf("版本位 = %c，想要 4: %q", id[12], id)
		}
		if !containsRune("89ab", id[16]) {
			t.Errorf("变体位 = %c，想要 8/9/a/b: %q", id[16], id)
		}
		if seen[id] {
			t.Fatalf("出现了重复 id: %q", id)
		}
		seen[id] = true
	}
}

func containsRune(set string, want byte) bool {
	for i := 0; i < len(set); i++ {
		if set[i] == want {
			return true
		}
	}
	return false
}
