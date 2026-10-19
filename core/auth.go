package core

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	randv2 "math/rand/v2"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id 的默认档：m=19 MiB, t=2, p=1。改这几个值等于让旧哈希全部失效。
const (
	argonMemory  uint32 = 19456
	argonTime    uint32 = 2
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// 一次校验要 19 MiB，同时最多算 4 个：并发登录不能把内存顶穿。
var argonSlots = make(chan struct{}, 4)

// 登录校验的最短耗时：1 秒再加 0~200ms 抖动 —— 固定值本身也是可观测的指纹。
const (
	loginMinSeconds = time.Second
	loginJitterMax  = 200 * time.Millisecond
)

// LoginFloor 给出本次校验该等多久。
func LoginFloor() time.Duration {
	return loginMinSeconds + time.Duration(randv2.Int64N(int64(loginJitterMax)+1))
}

// SleepUntil 把一次校验补到 `min` 那么长，抹掉「token 不对就秒回」这个时序侧信道。
func SleepUntil(start time.Time, min time.Duration) {
	if remaining := min - time.Since(start); remaining > 0 {
		time.Sleep(remaining)
	}
}

// PHC 串里的 base64 不带填充（与 argon2-cffi 一致）。
var phcBase64 = base64.RawStdEncoding

// HashToken 把明文令牌变成 Argon2id PHC 串：
// `$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>`。启动时算一次，进程里只留摘要。
func HashToken(token string) (string, error) {
	salt, err := RandomBytes(argonSaltLen)
	if err != nil {
		return "", err
	}
	digest := argonToken(token, salt, argonMemory, argonTime, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		phcBase64.EncodeToString(salt), phcBase64.EncodeToString(digest)), nil
}

// VerifyToken 按摘要里记着的参数与盐把候选令牌再哈希一遍，恒定时间比较。
func VerifyToken(token, encoded string) (bool, error) {
	salt, digest, memory, iterations, threads, err := parseTokenHash(encoded)
	if err != nil {
		return false, err
	}
	candidate := argonToken(token, salt, memory, iterations, threads, uint32(len(digest)))
	return subtle.ConstantTimeCompare(candidate, digest) == 1, nil
}

// argonToken 是所有 Argon2id 计算的唯一入口，并发上限在这里统一收口。
func argonToken(token string, salt []byte, memory, iterations uint32, threads uint8, length uint32) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey([]byte(token), salt, iterations, memory, threads, length)
}

func parseTokenHash(encoded string) (salt, digest []byte, memory, iterations uint32, threads uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, fmt.Errorf("token hash is not an argon2id PHC string")
	}
	var version int
	if _, scanErr := fmt.Sscanf(parts[2], "v=%d", &version); scanErr != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, fmt.Errorf("unsupported argon2 version %q", parts[2])
	}
	if _, scanErr := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); scanErr != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad argon2 parameters %q", parts[3])
	}
	// 参数上下限挡住「把哈希串改成 m=4GiB」这类坏数据：内存与 CPU 都不能被放大。
	if memory < 8*1024 || memory > 1024*1024 || iterations < 1 || iterations > 10 || threads < 1 || threads > 8 {
		return nil, nil, 0, 0, 0, fmt.Errorf("argon2 parameters out of range: m=%d,t=%d,p=%d", memory, iterations, threads)
	}
	salt, decodeErr := phcBase64.DecodeString(parts[4])
	if decodeErr != nil || len(salt) < 8 {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad argon2 salt")
	}
	digest, decodeErr = phcBase64.DecodeString(parts[5])
	// 摘要段为空时 ConstantTimeCompare 对两个空切片也返回相等，等于放行任何令牌。
	if decodeErr != nil || len(digest) < 16 {
		return nil, nil, 0, 0, 0, fmt.Errorf("bad argon2 digest")
	}
	return salt, digest, memory, iterations, threads, nil
}

func RandomBytes(size int) ([]byte, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return nil, fmt.Errorf("random source unavailable: %w", err)
	}
	return buffer, nil
}
