package core

import "encoding/hex"

// UUID4 生成 uuid4：32 位小写十六进制、无连字符（文件记录的 id 就是这个形状）。
func UUID4() (string, error) {
	value, err := RandomBytes(16)
	if err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40 // version 4
	value[8] = (value[8] & 0x3f) | 0x80 // RFC 4122 variant
	return hex.EncodeToString(value), nil
}
