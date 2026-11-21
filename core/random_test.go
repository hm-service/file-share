package core

import "testing"

func TestRandomBytes(t *testing.T) {
	first, err := RandomBytes(16)
	if err != nil {
		t.Fatalf("RandomBytes() 报错: %v", err)
	}
	if len(first) != 16 {
		t.Fatalf("长度 = %d，想要 16", len(first))
	}
	second, err := RandomBytes(16)
	if err != nil {
		t.Fatalf("RandomBytes() 报错: %v", err)
	}
	if string(first) == string(second) {
		t.Error("两次取到同一串随机数")
	}
}
