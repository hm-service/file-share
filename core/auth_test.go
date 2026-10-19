package core

import (
	"strings"
	"testing"
	"time"
)

func TestHashTokenRoundTrip(t *testing.T) {
	hash, err := HashToken("correct_token_123")
	if err != nil {
		t.Fatalf("HashToken() 报错: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("哈希串不是约定的 PHC 形状: %q", hash)
	}

	ok, err := VerifyToken("correct_token_123", hash)
	if err != nil {
		t.Fatalf("VerifyToken() 报错: %v", err)
	}
	if !ok {
		t.Error("正确的令牌应当通过校验")
	}
	if ok, err := VerifyToken("wrong_token_456", hash); err != nil || ok {
		t.Errorf("错误令牌应当校验失败: ok=%v err=%v", ok, err)
	}
}

func TestHashTokenUsesFreshSalt(t *testing.T) {
	first, err := HashToken("same_token")
	if err != nil {
		t.Fatalf("HashToken() 报错: %v", err)
	}
	second, err := HashToken("same_token")
	if err != nil {
		t.Fatalf("HashToken() 报错: %v", err)
	}
	if first == second {
		t.Fatal("同一个令牌两次哈希不该相同（盐必须随机）")
	}
}

func TestVerifyTokenRejectsBrokenHash(t *testing.T) {
	cases := []struct {
		name string
		hash string
	}{
		{name: "空串", hash: ""},
		{name: "不是 PHC", hash: "plaintext"},
		{name: "算法不对", hash: "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"},
		{name: "版本不对", hash: "$argon2id$v=18$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"},
		{name: "参数段缺项", hash: "$argon2id$v=19$m=19456$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"},
		{name: "内存参数过小", hash: "$argon2id$v=19$m=1,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"},
		{name: "迭代参数过大", hash: "$argon2id$v=19$m=19456,t=99,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"},
		{name: "盐太短", hash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaGhhc2hoYXNoaGFzaA"},
		{name: "摘要为空", hash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$"},
		{name: "摘要太短", hash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaA"},
		{name: "base64 坏了", hash: "$argon2id$v=19$m=19456,t=2,p=1$!!!$%%%"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			ok, err := VerifyToken("anything", item.hash)
			if err == nil {
				t.Fatalf("VerifyToken(%q) 想要报错，却成功了（ok=%v）", item.hash, ok)
			}
			if ok {
				t.Error("坏哈希串不该校验通过")
			}
		})
	}
}

// 登录校验的最短耗时是 1s + rand[0,200ms]；只抽两次，免得测试本身跑得太慢。
func TestLoginFloor(t *testing.T) {
	for i := 0; i < 2; i++ {
		floor := LoginFloor()
		if floor < time.Second || floor > time.Second+201*time.Millisecond {
			t.Fatalf("LoginFloor() = %v，不在 [1s, 1.2s] 内", floor)
		}
	}
}

func TestSleepUntil(t *testing.T) {
	start := time.Now()
	SleepUntil(start, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Errorf("SleepUntil() 只等了 %v，想要至少 30ms", elapsed)
	}

	start = time.Now().Add(-time.Second)
	before := time.Now()
	SleepUntil(start, 10*time.Millisecond)
	if elapsed := time.Since(before); elapsed > 5*time.Millisecond {
		t.Errorf("已经超时的情况下 SleepUntil() 不该再等，却等了 %v", elapsed)
	}
}

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
		t.Error("两次随机数不该相同")
	}
}
