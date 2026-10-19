// Package httpx 是 HTTP 小工具：JSON 响应、错误响应、状态码记录器与客户端 IP 解析。
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ContentType 是 /api/* 与 /s/* 的 JSON 响应类型。
const ContentType = "application/json; charset=utf-8"

// Detail 是错误体：前端只读 detail 字段。
type Detail struct {
	Detail string `json:"detail"`
}

// Write 写一个 JSON 响应。
func Write(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// 只有程序自己拼的响应会走到这里（业务数据都是简单结构），属于编码 bug。
		slog.Error("json encode failed", slog.Any("error", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", ContentType)
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(body)
}

// Fail 写统一的错误体 {"detail": "…"}。
func Fail(w http.ResponseWriter, status int, detail string) {
	Write(w, status, Detail{Detail: detail})
}

// Unauthorized 是 401 的固定写法：带 WWW-Authenticate: Bearer。
func Unauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	Fail(w, http.StatusUnauthorized, detail)
}
