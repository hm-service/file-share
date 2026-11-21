package core

import (
	"testing"
	"time"
)

func TestLoginFloor(t *testing.T) {
	for i := 0; i < 20; i++ {
		floor := LoginFloor()
		if floor < time.Second || floor > 1200*time.Millisecond {
			t.Fatalf("LoginFloor() = %v，不在 [1s, 1.2s] 内", floor)
		}
	}
}

func TestSleepUntil(t *testing.T) {
	start := time.Now()
	SleepUntil(start, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("SleepUntil() 只等了 %v，想要至少 30ms", elapsed)
	}

	// 已经超过下限时不该再补等：先睡 20ms，再要求至少 10ms，总耗时应当仍在 20ms 附近。
	start = time.Now()
	time.Sleep(20 * time.Millisecond)
	SleepUntil(start, 10*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 25*time.Millisecond {
		t.Errorf("已经超时的情况下 SleepUntil() 不该再等，却等了 %v", elapsed)
	}
}
