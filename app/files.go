package app

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"file-share/core"
)

// ListFiles 返回只读目录里的全部文件，带上收藏标记。
func (a *App) ListFiles() ([]File, error) {
	snapshot, err := a.scan()
	if err != nil {
		slog.Error("scan files failed", slog.Any("error", err))
		return nil, core.Fail(http.StatusInternalServerError, "store.files")
	}
	favorites := a.favorites.All()
	for i := range snapshot.files {
		snapshot.files[i].IsFavorite = favorites[snapshot.files[i].ID]
	}
	return snapshot.files, nil
}

// ListGroups 返回只读目录里的全部分组（含空目录），按父路径与名称排序。
func (a *App) ListGroups() ([]Group, error) {
	snapshot, err := a.scan()
	if err != nil {
		slog.Error("scan groups failed", slog.Any("error", err))
		return nil, core.Fail(http.StatusInternalServerError, "store.groups")
	}
	return snapshot.groups, nil
}

// DownloadedFile 是一次下载打开的文件：ServeContent 需要可 Seek 的句柄与元数据。
type DownloadedFile struct {
	Name    string
	Size    int64
	ModTime time.Time
	io.ReadSeekCloser
}

// OpenDownload 按相对路径打开文件供下载。
func (a *App) OpenDownload(rel string) (*DownloadedFile, error) {
	file, info, err := a.openFile(rel)
	if err != nil {
		return nil, fileFailure(err)
	}
	return &DownloadedFile{
		Name:           filepath.Base(rel),
		Size:           info.Size(),
		ModTime:        info.ModTime(),
		ReadSeekCloser: file,
	}, nil
}

// IssueDownloadURL 给一次页面内下载签一个短效 URL（10 分钟）。
func (a *App) IssueDownloadURL(rel string) (string, error) {
	return a.issueSignedURL(rel, core.SignedURLTTL)
}

// IssueShareLink 给分享链接签一个 ttl 内有效的 URL（10 分钟 / 24 小时两档）。
func (a *App) IssueShareLink(rel string, ttl time.Duration) (string, error) {
	return a.issueSignedURL(rel, ttl)
}

// issueSignedURL 校验路径存在后签发 /s/<escaped path> 地址。
func (a *App) issueSignedURL(rel string, ttl time.Duration) (string, error) {
	if err := validateRelPath(rel); err != nil {
		return "", fileFailure(err)
	}
	if _, _, err := a.openFile(rel); err != nil {
		return "", fileFailure(err)
	}
	exp := core.SignedURLExpiry(time.Now(), ttl)
	return a.signurl.SignedURL(escapedDownloadPath(rel), exp), nil
}

// OpenSignedDownload 校验签名 URL 后打开文件：方法、转义路径与过期时间都在签名里。
func (a *App) OpenSignedDownload(rel, method, escapedPath, sig string, exp int64) (*DownloadedFile, error) {
	if err := a.signurl.Verify(method, escapedPath, sig, exp, time.Now()); err != nil {
		if errors.Is(err, core.ErrSignedURLExpired) {
			return nil, core.Fail(http.StatusUnauthorized, "download.expired")
		}
		return nil, core.Fail(http.StatusForbidden, "download.invalid-signature")
	}
	return a.OpenDownload(rel)
}

// ToggleFavorite 切换一个相对路径的收藏状态，返回切换后的状态。
func (a *App) ToggleFavorite(rel string) (bool, error) {
	favorite, err := a.favorites.Toggle(rel)
	if err != nil {
		if errors.Is(err, ErrInvalidPath) {
			return false, core.Fail(http.StatusBadRequest, "file.invalid-path")
		}
		slog.Error("write favorites failed", slog.Any("error", err))
		return false, core.Fail(http.StatusInternalServerError, "store.favorites")
	}
	return favorite, nil
}

// escapedDownloadPath 把相对路径转成 /s/ 下的下载路径：逐段 PathEscape，段之间保留 "/"。
// 中文、空格、括号都按标准百分号编码，签名覆盖的就是这个逐字节一致的转义路径。
func escapedDownloadPath(rel string) string {
	segments := strings.Split(rel, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/s/" + strings.Join(segments, "/")
}

// fileFailure 把只读目录的错误翻译成对外错误：不存在 404、路径非法 400、其余 500。
func fileFailure(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return core.Fail(http.StatusNotFound, "file.not-found")
	case errors.Is(err, ErrInvalidPath):
		return core.Fail(http.StatusBadRequest, "file.invalid-path")
	}
	return err
}
