package core

import "unicode/utf8"

// MaxFilenameBytes 是展示 / 下载文件名的上限：多数文件系统单个组件的上限就是 255 字节。
const MaxFilenameBytes = 255

// IsSHA256 判断是否 64 位小写十六进制。大写不算合法：客户端与服务端都按小写比较，
// 允许大小写混写只会让「同一个内容两个字符串」的歧义漏进库里。
func IsSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ValidFilename 判断上传文件名能否直接落库、回给浏览器：只接受 basename、<=255 字节、
// 合法 UTF-8、不含控制字符与路径分隔符。控制字符会让响应头（Content-Disposition）
// 与日志变得不可读，路径分隔符则意味着客户端给的不是「名字」而是路径。
func ValidFilename(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > MaxFilenameBytes {
		return false
	}
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
