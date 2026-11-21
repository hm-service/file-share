package api

import (
	"net/http"

	"file-share/httpx"
	"file-share/i18n"
)

// storageResponse 是 /api/storage 的响应：存放方案 + 后端此刻是否可达。
// endpoint 与凭据不出服务端；healthy 每次请求实时探测，界面据此显示绿 / 红点。
type storageResponse struct {
	Backend string `json:"backend"`
	Bucket  string `json:"bucket,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

// handleStorage 返回当前 blob 的存放方案（本地目录或 S3 对象存储），供界面提示用。
// 只回后端类型、桶名与前缀：endpoint 和凭据不出服务端。
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	resp := storageResponse{
		Backend: s.App.Storage.Backend,
		Bucket:  s.App.Storage.Bucket,
		Prefix:  s.App.Storage.Prefix,
		Healthy: true,
	}
	if err := s.App.CheckStorage(r.Context()); err != nil {
		resp.Healthy = false
		resp.Error = i18n.T(language(r), "storage.unavailable")
	}
	httpx.Write(w, http.StatusOK, resp)
}
