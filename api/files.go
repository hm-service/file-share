package api

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"file-share/app"
	"file-share/core"
	"file-share/httpx"
)

// handleListFiles 返回全部记录与全部分组：文件按 sort_order 与创建时间排序，
// groups 带完整路径与父路径，顺序即侧栏 / 移动端 chips 的展示顺序。
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.App.ListFiles()
	if err != nil {
		businessFail(w, r, err)
		return
	}
	groups, err := s.App.ListGroups()
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"files": files, "groups": groups})
}

// handleCheckFile 是秒传预检：blob 已在服务器上就补一条记录并回 exists=true。
func (s *Server) handleCheckFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(MaxCheckFileBodyBytes); err != nil {
		if isBodyTooLarge(err) {
			fail(w, r, http.StatusRequestEntityTooLarge, "request.too-big")
			return
		}
		fail(w, r, http.StatusBadRequest, "upload.missing")
		return
	}
	defer r.MultipartForm.RemoveAll()

	record, exists, err := s.App.CheckFile(
		strings.TrimSpace(r.PostFormValue("client_sha256")),
		r.PostFormValue("filename"),
	)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	if !exists {
		httpx.Write(w, http.StatusOK, map[string]any{"exists": false})
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{
		"exists":   true,
		"id":       record.ID,
		"filename": record.Filename,
		"size":     record.Size,
	})
}

// handleUpload 流式接收 multipart 上传。字段顺序不固定：遇到 file 字段必须立刻落盘，
// 不能等后面的 client_sha256 —— 否则 NextPart 会把已经读过的内容丢掉。
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, r, http.StatusBadRequest, "upload.missing")
		return
	}

	var (
		upload    *app.Upload
		filename  string
		clientSHA string
		hasFile   bool
	)
	// 临时文件在任何错误路径上都要清理；成功落成 blob 后 Abort 是空操作。
	defer func() {
		if upload != nil {
			upload.Abort()
		}
	}()

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if isBodyTooLarge(err) {
				fail(w, r, http.StatusRequestEntityTooLarge, "upload.too-big")
			} else {
				fail(w, r, http.StatusBadRequest, "upload.missing")
			}
			return
		}
		switch part.FormName() {
		case "client_sha256":
			value, err := io.ReadAll(io.LimitReader(part, 256))
			if err != nil {
				if isBodyTooLarge(err) {
					fail(w, r, http.StatusRequestEntityTooLarge, "upload.too-big")
				} else {
					fail(w, r, http.StatusBadRequest, "upload.missing")
				}
				return
			}
			clientSHA = strings.TrimSpace(string(value))
		case "file":
			if hasFile {
				// 契约只约定一个 file 字段；多出来的内容丢掉，不覆盖已经写下的那份。
				if _, err := io.Copy(io.Discard, part); err != nil {
					fail(w, r, http.StatusBadRequest, "upload.missing")
					return
				}
				continue
			}
			filename = part.FileName()
			// 先校验文件名再落盘：无效名字没必要把整份内容写完再拒。
			if !core.ValidFilename(filename) {
				fail(w, r, http.StatusBadRequest, "upload.invalid-filename")
				return
			}
			hasFile = true
			upload, err = s.App.NewUpload()
			if err != nil {
				slog.ErrorContext(r.Context(), "create upload temp file failed", slog.Any("error", err))
				fail(w, r, http.StatusInternalServerError, "upload.write")
				return
			}
			if _, err := io.Copy(upload, part); err != nil {
				var business *core.Error
				switch {
				case errors.As(err, &business):
					businessFail(w, r, err)
				case isBodyTooLarge(err):
					fail(w, r, http.StatusRequestEntityTooLarge, "upload.too-big")
				case upload.WriteFailed():
					slog.ErrorContext(r.Context(), "write upload temp file failed", slog.Any("error", err))
					fail(w, r, http.StatusInternalServerError, "upload.write")
				default:
					fail(w, r, http.StatusBadRequest, "upload.missing")
				}
				return
			}
		default:
			if _, err := io.Copy(io.Discard, part); err != nil {
				fail(w, r, http.StatusBadRequest, "upload.missing")
				return
			}
		}
	}

	if !hasFile {
		fail(w, r, http.StatusBadRequest, "upload.missing")
		return
	}
	record, err := s.App.FinishUpload(upload, filename, clientSHA)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"id": record.ID, "sha256": record.SHA256})
}

// handleUpdateFile 按需改写文件名（重命名）与分组路径；成功后响应更新后的完整记录。
func (s *Server) handleUpdateFile(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Filename  *string `json:"filename"`
		GroupPath *string `json:"group_path"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	record, err := s.App.UpdateFile(r.PathValue("file_id"), payload.Filename, payload.GroupPath)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, record)
}

// handleSetGroupOrder 按拖拽后的顺序重写某个父级下子分组的顺序；成功回 204。
func (s *Server) handleSetGroupOrder(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Parent string   `json:"parent"`
		Names  []string `json:"names"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.App.SetGroupOrder(payload.Parent, payload.Names); err != nil {
		businessFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateGroup 手动新建一个空分组；成功 201 并返回规范化后的分组路径。
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	path, err := s.App.CreateGroup(payload.Path)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusCreated, map[string]any{"path": path})
}

// handleRenameGroup 改写一个分组路径的最后一段；同级位置不变，成功回 204。
func (s *Server) handleRenameGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path    string `json:"path"`
		NewName string `json:"new_name"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.App.RenameGroup(payload.Path, payload.NewName); err != nil {
		businessFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteGroup 删除一个分组（子分组一并删除）；delete_files 决定组内文件是一起删还是上移。
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path        string `json:"path"`
		DeleteFiles bool   `json:"delete_files"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.App.DeleteGroup(payload.Path, payload.DeleteFiles); err != nil {
		businessFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetOrder 批量重写列表顺序，成功回 204（无响应体）。
func (s *Server) handleSetOrder(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Items []core.OrderItem `json:"items"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.App.SetOrder(payload.Items); err != nil {
		businessFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	favorite, err := s.App.ToggleFavorite(r.PathValue("file_id"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"is_favorite": favorite})
}

func (s *Server) handlePublic(w http.ResponseWriter, r *http.Request) {
	isPublic, err := s.App.TogglePublic(r.PathValue("file_id"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"is_public": isPublic})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	download, err := s.App.OpenDownload(r.PathValue("file_id"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

// handlePublicDownload 是匿名下载路由：私有与不存在对外表现完全一致。
func (s *Server) handlePublicDownload(w http.ResponseWriter, r *http.Request) {
	download, err := s.App.OpenPublicDownload(r.PathValue("file_id"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

// serveDownload 发附件。http.ServeContent 负责 Range / HEAD / Content-Length。
func serveDownload(w http.ResponseWriter, r *http.Request, download *app.DownloadedFile) {
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	// FormatMediaType 对非 ASCII 文件名自动改走 RFC 2231 的 filename* 形式。
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.File.Filename}))
	http.ServeContent(w, r, download.File.Filename, time.Time{}, download.Blob)
}

// handleFileDownloadTicket 签一张 60 秒有效的下载票据。前端拿 url 后让浏览器直接导航：
// 流式下载、Range、原生进度都在浏览器侧完成，服务端不用把整份文件读进内存。
func (s *Server) handleFileDownloadTicket(w http.ResponseWriter, r *http.Request) {
	ticket, err := s.App.IssueFileDownloadTicket(r.PathValue("file_id"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"url": ticketURL(ticket)})
}

// handleTicketDownload 是浏览器导航入口：票据绑定单个文件，不需要 Authorization ——
// 凭据就是票据本身。分组「全部下载」由前端逐个文件调用这里，服务端不提供打包。
func (s *Server) handleTicketDownload(w http.ResponseWriter, r *http.Request) {
	fileID, err := s.App.VerifyDownloadTicket(r.URL.Query().Get("t"))
	if err != nil {
		businessFail(w, r, err)
		return
	}
	download, err := s.App.OpenDownload(fileID)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

// ticketURL 拼出票据下载地址。票据是 base64url，字符集天然 URL 安全；
// 票据放在查询参数而不是路径里，访问日志只记 /d，不会把凭据写进日志。
func ticketURL(ticket string) string {
	return "/d?t=" + ticket
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.App.DeleteFile(r.PathValue("file_id")); err != nil {
		businessFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
