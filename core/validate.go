package core

import (
	"strings"
	"unicode/utf8"
)

// MaxFilenameBytes 是展示 / 下载文件名的上限：多数文件系统单个组件的上限就是 255 字节。
const MaxFilenameBytes = 255

// MaxGroupNameBytes 是分组名（路径里的一段）的上限（UTF-8 字节数）：分组只是标签，64 字节够用。
const MaxGroupNameBytes = 64

// MaxGroupDepth 是分组层数上限：只支持顶级与二级（"父/子"），三级及以上一律拒绝。
const MaxGroupDepth = 2

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
	"d":           {},
	"auth":        {},
	"assets":      {},
	"readyz":      {},
	"livez":       {},
	"favorites":   {},
	"ungrouped":   {},
	"favicon.svg": {},
}

// NormalizeGroupPath 校验并规范化分组路径：空串表示未分组，其余形如 "顶级" 或 "顶级/二级"。
// 每段按 NormalizeGroupSegment 规则规范化（去掉首尾空白），层数不超过 MaxGroupDepth。
// 第二段起不参与顶层路由，因此只有第一段受 reservedGroupNames 约束。
func NormalizeGroupPath(raw string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", true
	}
	segments := strings.Split(path, "/")
	if len(segments) > MaxGroupDepth {
		return "", false
	}
	for i, segment := range segments {
		name, ok := NormalizeGroupSegment(segment)
		if !ok {
			return "", false
		}
		if i == 0 {
			if _, reserved := reservedGroupNames[name]; reserved {
				return "", false
			}
		}
		segments[i] = name
	}
	return strings.Join(segments, "/"), true
}

// NormalizeGroupSegment 校验并规范化路径里的一段名字：去掉首尾空白，不允许超过
// MaxGroupNameBytes 字节、含控制字符或 "/"。
//
// 分组名同时是页面路径的一段，所以 "." / ".." 和以 .html 结尾的名字也不能用
// （这些路径由 ServeMux 清理规则与页面表占用）。
func NormalizeGroupSegment(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > MaxGroupNameBytes || !utf8.ValidString(name) {
		return "", false
	}
	if name == "." || name == ".." || strings.HasSuffix(name, ".html") {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' {
			return "", false
		}
	}
	return name, true
}

// SplitGroupPath 把规范化路径拆成父路径与最后一段；顶级分组的父路径是空串。
func SplitGroupPath(path string) (parent, name string) {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

// ReservedGroupRoot 判断某个名字是不是站点已占用的一级路径（不能拿来当顶级分组名）。
func ReservedGroupRoot(name string) bool {
	_, reserved := reservedGroupNames[name]
	return reserved
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
