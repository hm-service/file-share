// Package i18n 是后端文案表：用户可见的文案都从这里取。
//
// 源语言是简体中文，英文是另一栏；语言由请求的 Accept-Language 决定，
// 不落库、不跟账号。未知键原样返回键名，漏配一眼就能看出来。
package i18n

import (
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
	"auth.required":              {"需要登录", "Sign-in required"},
	"auth.invalid":               {"用户名或密码错误", "Wrong username or password"},
	"auth.expired":               {"登录已过期，请重新登录", "Session expired, sign in again"},
	"auth.malformed":             {"登录凭据格式无效", "Invalid session credential"},
	"permission.denied":          {"当前账号没有该操作权限", "This account is not allowed to do that"},
	"request.json":               {"请求体不是合法的 JSON", "Malformed JSON body"},
	"request.too-big":            {"请求体过大", "Request body is too large"},
	"file.not-found":             {"找不到该文件", "File not found"},
	"file.invalid-path":          {"文件路径无效", "Invalid file path"},
	"download.invalid":           {"下载链接无效或已过期", "Download link is invalid or expired"},
	"download.expired":           {"下载链接已过期", "Download link expired"},
	"download.invalid-signature": {"下载链接签名无效", "Invalid download signature"},
	"share.invalid-ttl":          {"分享链接有效期无效", "Invalid share link lifetime"},
	"store.files":                {"读取文件列表失败", "Failed to list files"},
	"store.groups":               {"读取分组列表失败", "Failed to list groups"},
	"store.favorites":            {"保存收藏失败", "Failed to save favorites"},
	"internal":                   {"服务器内部错误", "Internal server error"},
	"storage.unavailable":        {"存储服务暂时不可用，请稍后重试", "Storage service is temporarily unavailable, please retry later"},
	"not-found":                  {"资源不存在", "Not found"},
	"method-not-allowed":         {"方法不允许", "Method not allowed"},
	"session.issue":              {"签发登录凭据失败", "Failed to issue a session credential"},
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
