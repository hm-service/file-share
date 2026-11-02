package api

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"file-share/core"
)

// serveGroupZip 把一个分组内的文件按列表顺序打进一个 zip 流式发出。
//
// 条目用 Store（不重新压缩）：分组里多是照片、安装包这类已经压不动的文件，
// 现场 Deflate 只会白耗 CPU；流式输出也就没有 Content-Length，进度由浏览器按已收字节显示。
func (s *Server) serveGroupZip(w http.ResponseWriter, r *http.Request, groupName string) {
	files, err := s.App.GroupFiles(groupName)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	if len(files) == 0 {
		businessFail(w, r, core.Fail(404, "group.not-found"))
		return
	}

	header := w.Header()
	header.Set("Content-Type", "application/zip")
	header.Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": groupName + ".zip"}))
	w.WriteHeader(http.StatusOK)

	archive := zip.NewWriter(w)
	used := make(map[string]struct{}, len(files))
	for _, file := range files {
		download, err := s.App.OpenDownload(file.ID)
		if err != nil {
			// 响应头已经发出，改不了状态码；中断连接让客户端明确看到失败，
			// 而不是收到一个悄悄缺了文件的 zip。
			slog.ErrorContext(r.Context(), "open file for group zip failed",
				slog.String("file_id", file.ID), slog.Any("error", err))
			return
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{
			Name:     uniqueArchiveName(file.Filename, used),
			Method:   zip.Store,
			Modified: time.Unix(file.CreatedAt, 0),
		})
		if err != nil {
			download.Close()
			slog.ErrorContext(r.Context(), "create zip entry failed", slog.Any("error", err))
			return
		}
		_, copyErr := io.Copy(entry, download.Blob)
		download.Close()
		if copyErr != nil {
			slog.ErrorContext(r.Context(), "copy file into group zip failed",
				slog.String("file_id", file.ID), slog.Any("error", copyErr))
			return
		}
	}
	if err := archive.Close(); err != nil {
		slog.ErrorContext(r.Context(), "close group zip failed", slog.Any("error", err))
	}
}

// uniqueArchiveName 处理同组重名：第一个保留原名，后续依次变成「名称 (2).ext」。
// 文件名本身在上传时已校验过不含路径分隔符，这里只解决重名，不需要再考虑目录穿越。
func uniqueArchiveName(name string, used map[string]struct{}) string {
	if _, exists := used[name]; !exists {
		used[name] = struct{}{}
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
	}
}
