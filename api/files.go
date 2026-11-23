package api

import (
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"file-share/app"
	"file-share/core"
	"file-share/httpx"
)

// handleListFiles 返回当前用户可见根下的全部文件与分组：文件按（分组、名称）排序，
// 分组带完整路径与父路径，顺序即侧栏 / 移动端 chips 的展示顺序。
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	files, err := s.App.ListFiles(user)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	groups, err := s.App.ListGroups(user)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{
		"files":  files,
		"groups": groups,
	})
}

// handleFavorite 切换收藏：PUT 收藏、DELETE 取消，路径走 ?path=。
// 收藏是本地元数据，不检查文件是否存在（文件被 SMB 侧删掉时记录自然匹配不到）。
func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	favorite, err := s.App.ToggleFavorite(currentUser(r), r.URL.Query().Get("path"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"is_favorite": favorite})
}

// handleDownloadURL 签一个匿名下载地址（SignedURLTTL 内有效）。前端拿 url 后让浏览器
// 直接导航：流式下载、Range、原生进度都在浏览器侧完成，服务端不用把整份文件读进内存。
func (s *Server) handleDownloadURL(w http.ResponseWriter, r *http.Request) {
	link, err := s.App.IssueDownloadURL(currentUser(r), r.URL.Query().Get("path"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"url": link})
}

// handleShareLink 签一条 ttl 内有效的分享链接：对方免登录即可下载，签名不对或过期
// 一律按「链接无效」处理。ttl 只认默认档与 24 小时档（见 shareLinkTTL），其它值直接 400。
func (s *Server) handleShareLink(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path string `json:"path"`
		TTL  string `json:"ttl"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	ttl, ok := shareLinkTTL(payload.TTL)
	if !ok {
		fail(w, r, http.StatusBadRequest, "share.invalid-ttl")
		return
	}
	link, err := s.App.IssueShareLink(currentUser(r), payload.Path, ttl)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"url": link})
}

// shareLinkTTL 把 ttl 换成有效期：不传走默认 10 分钟，只额外认 24 小时档。
// 不接受任意秒数 —— 分享链接的有效期是产品决定，不该由调用方随手拉长或缩短。
func shareLinkTTL(raw string) (time.Duration, bool) {
	switch raw {
	case "", "10m", "600":
		return core.SignedURLTTL, true
	case "24h", "86400":
		return core.ShareLinkTTL24h, true
	}
	return 0, false
}

// handleSignedDownload 是匿名下载入口：凭 URL 里的 sig/exp 下载单个文件，不需要 Authorization ——
// 凭据就是签名本身。
func (s *Server) handleSignedDownload(w http.ResponseWriter, r *http.Request) {
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil {
		fail(w, r, http.StatusNotFound, "download.invalid")
		return
	}
	rel, ok := downloadRelPath(r.URL.EscapedPath())
	if !ok {
		fail(w, r, http.StatusNotFound, "download.invalid")
		return
	}
	download, err := s.App.OpenSignedDownload(
		rel, r.Method, r.URL.EscapedPath(), r.URL.Query().Get("sig"), exp)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

// downloadRelPath 从 /s/<escaped path> 反解出相对路径：逐段 PathUnescape 后重新用 "/" 连接。
func downloadRelPath(escapedPath string) (string, bool) {
	const prefix = "/s/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(escapedPath, prefix)
	if rest == "" {
		return "", false
	}
	segments := strings.Split(rest, "/")
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "" {
			return "", false
		}
		segments[i] = decoded
	}
	return strings.Join(segments, "/"), true
}

// serveDownload 发附件。http.ServeContent 负责 Range / HEAD / Content-Length。
func serveDownload(w http.ResponseWriter, r *http.Request, download *app.DownloadedFile) {
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	// FormatMediaType 对非 ASCII 文件名自动改走 RFC 2231 的 filename* 形式。
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.Name}))
	http.ServeContent(w, r, download.Name, download.ModTime, download.ReadSeekCloser)
}
