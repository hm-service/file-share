package core

import (
	"strings"
	"unicode/utf8"
)

// MaxFilenameBytes 是展示 / 下载文件名的上限：多数文件系统单个组件的上限就是 255 字节。
const MaxFilenameBytes = 255

// MaxGroupNameBytes 是分组名的上限（UTF-8 字节数）：分组只是一级标签，64 字节够用。
const MaxGroupNameBytes = 64

// 批量排序输入的边界：一次最多重排 2000 条，sort_order 落在 [0, 1e9]。
const (
	MaxOrderItems = 2000
	MaxSortOrder  = 1_000_000_000
)

// OrderItem 是批量排序里的一项：把某条记录放到 sort_order 这个位置。
type OrderItem struct {
	ID        string `json:"id"`
	SortOrder int64  `json:"sort_order"`
}

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

// reservedGroupNames 是站点已经占用的一级路径。分组名会直接作为 /<分组名> 出现在地址栏，
// 和这些路径重名会让链接指到别处（接口、登录页、静态资源），所以在命名时就拒绝。
//
// 名单要和 main.go 的路由、web.go 的页面表以及前端的视图路由保持一致。
var reservedGroupNames = map[string]struct{}{
	"api":         {},
	"s":           {},
	"d":           {},
	"auth":        {},
	"assets":      {},
	"readyz":      {},
	"livez":       {},
	"favorites":   {},
	"ungrouped":   {},
	"favicon.svg": {},
}

// NormalizeGroupName 校验并规范化分组名：去掉首尾空白，空串表示未分组；
// 规范化后不允许超过 64 字节、含控制字符或路径分隔符。第二个返回值表示是否合法。
//
// 分组名同时是页面路径的一段，所以 "." / ".."、以 .html 结尾（这些路径由页面表与产物文件
// 占用）和 reservedGroupNames 里的名字都不能用。
//
// 先 trim 再判长度：客户端在输入框里多敲的空白不该被当成名字的一部分。
func NormalizeGroupName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if len(name) > MaxGroupNameBytes || !utf8.ValidString(name) {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' {
			return "", false
		}
	}
	if name == "." || name == ".." || strings.HasSuffix(name, ".html") {
		return "", false
	}
	if _, reserved := reservedGroupNames[name]; reserved {
		return "", false
	}
	return name, true
}

// ValidOrderItems 校验批量排序输入：1..2000 项、每条 id 非空且不重复、
// sort_order 在 [0, MaxSortOrder]。
func ValidOrderItems(items []OrderItem) bool {
	if len(items) < 1 || len(items) > MaxOrderItems {
		return false
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ID == "" || item.SortOrder < 0 || item.SortOrder > MaxSortOrder {
			return false
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return true
}
