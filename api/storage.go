package api

import (
	"net/http"

	"file-share/httpx"
)

// handleStorage 返回当前 blob 的存放方案（本地目录或 S3 对象存储），供界面提示用。
// 只回后端类型、桶名与前缀：endpoint 和凭据不出服务端。
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	httpx.Write(w, http.StatusOK, s.App.Storage)
}
