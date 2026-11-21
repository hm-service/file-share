package core

import (
	"crypto/rand"
	"fmt"
)

// RandomBytes 从系统随机源取 size 字节；取不到就直接报错，不退回可预测的伪随机。
func RandomBytes(size int) ([]byte, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return nil, fmt.Errorf("random source unavailable: %w", err)
	}
	return buffer, nil
}
