// Package i18n 是后端文案表：用户可见的文案都从这里取。
//
// 源语言是简体中文，英文是另一栏；语言由请求的 Accept-Language 决定，
// 不落库、不跟账号。未知键原样返回键名，漏配一眼就能看出来。
package i18n

import (
	"fmt"
	"strings"
)

type Lang string

const (
	ZhHans Lang = "zh-Hans"
	En     Lang = "en"
)

// Default 是认不出请求语言时的兜底，也是源语言。
const Default = ZhHans

type entry struct {
	zh string
	en string
}

var catalog = map[string]entry{
	"auth.required":            {"需要登录", "Sign-in required"},
	"auth.invalid":             {"认证令牌不正确", "Incorrect access token"},
	"auth.expired":             {"登录已过期，请重新登录", "Session expired, sign in again"},
	"auth.malformed":           {"登录凭据格式无效", "Invalid session credential"},
	"request.json":             {"请求体不是合法的 JSON", "Malformed JSON body"},
	"request.too-big":          {"请求体过大", "Request body is too large"},
	"upload.missing":           {"缺少上传文件", "Missing uploaded file"},
	"upload.invalid-filename":  {"文件名无效", "Invalid file name"},
	"upload.invalid-sha256":    {"文件指纹格式无效", "Invalid file checksum"},
	"upload.checksum-mismatch": {"文件校验失败，上传内容可能已损坏", "Checksum mismatch; the upload may be corrupted"},
	"upload.too-big":           {"文件超过上传上限", "File exceeds the upload limit"},
	"upload.write":             {"写入文件失败", "Failed to write the file"},
	"file.not-found":           {"找不到该文件", "File not found"},
	"store.list":               {"读取文件列表失败", "Failed to list files"},
	"store.insert":             {"保存文件记录失败", "Failed to save the file record"},
	"store.favorite":           {"切换收藏失败", "Failed to toggle favorite"},
	"store.public":             {"切换公开状态失败", "Failed to toggle visibility"},
	"store.delete":             {"删除文件失败", "Failed to delete the file"},
	"internal":                 {"服务器内部错误", "Internal server error"},
	"not-found":                {"资源不存在", "Not found"},
	"method-not-allowed":       {"方法不允许", "Method not allowed"},
	"session.issue":            {"签发登录凭据失败", "Failed to issue a session credential"},
}

// Parse 从 Accept-Language 里挑一档支持的语言：按出现顺序找第一个 zh* / en*，
// 一个都不认得就用默认（也是源语言）。
func Parse(header string) Lang {
	for part := range strings.SplitSeq(header, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case strings.HasPrefix(tag, "en"):
			return En
		case strings.HasPrefix(tag, "zh"):
			return ZhHans
		}
	}
	return Default
}

// T 取文案；键不在表里就原样返回键名，不做 fallback。
func T(lang Lang, key string) string {
	item, ok := catalog[key]
	if !ok {
		return key
	}
	if lang == En {
		return item.en
	}
	return item.zh
}

// F 取文案并用 fmt 填占位符（后端占位符是 %d / %s 这一套）。
func F(lang Lang, key string, args ...any) string {
	template := T(lang, key)
	if len(args) == 0 {
		return template
	}
	return fmt.Sprintf(template, args...)
}
