package core

import (
	randv2 "math/rand/v2"
	"time"
)

// 登录校验的最短耗时：1 秒再加 0~200ms 抖动 —— 固定值本身也是可观测的指纹。
const (
	loginMinSeconds = time.Second
	loginJitterMax  = 200 * time.Millisecond
)

// LoginFloor 给出本次校验该等多久。
func LoginFloor() time.Duration {
	return loginMinSeconds + time.Duration(randv2.Int64N(int64(loginJitterMax)+1))
}

// SleepUntil 把一次校验补到 `min` 那么长，抹掉「密码不对就秒回」这个时序侧信道。
func SleepUntil(start time.Time, min time.Duration) {
	if remaining := min - time.Since(start); remaining > 0 {
		time.Sleep(remaining)
	}
}
