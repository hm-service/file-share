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

// handleListFiles 返回全部记录，按创建时间倒序。
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.App.ListFiles()
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"files": files})
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

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.App.DeleteFile(r.PathValue("file_id")); err != nil {
		businessFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
