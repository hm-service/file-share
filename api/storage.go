package api

import (
	"net/http"

	"file-share/httpx"
	"file-share/i18n"
)

// storageResponse 是 /api/storage 的响应：只读数据目录 + 收藏元数据是否可用。
// healthy 每次请求实时探测，界面据此显示绿 / 红点。
type storageResponse struct {
	// Backend 固定为 dir：当前存储方案是只读目录视图。
	Backend string `json:"backend"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

// handleStorage 返回当前存储方案的健康状态，供界面提示用。
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	resp := storageResponse{Backend: "dir", Healthy: true}
	if err := s.App.CheckStorage(r.Context()); err != nil {
		resp.Healthy = false
		resp.Error = i18n.T(language(r), "storage.unavailable")
	}
	httpx.Write(w, http.StatusOK, resp)
}
