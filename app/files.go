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

// ListFiles 返回当前用户可见范围内的全部文件，带上该用户自己的收藏标记。
func (a *App) ListFiles(user User) ([]File, error) {
	snapshot, err := a.scan(user)
	if err != nil {
		slog.Error("scan files failed", slog.Any("error", err))
		return nil, core.Fail(http.StatusInternalServerError, "store.files")
	}
	favorites := a.favoritesFor(user).All()
	for i := range snapshot.files {
		snapshot.files[i].IsFavorite = favorites[snapshot.files[i].ID]
	}
	return snapshot.files, nil
}

// ListGroups 返回当前用户可见范围内的全部分组（含空目录），按父路径与名称排序。
func (a *App) ListGroups(user User) ([]Group, error) {
	snapshot, err := a.scan(user)
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

// openDownload 打开一个全局相对路径的文件供下载。
func (a *App) openDownload(rel string) (*DownloadedFile, error) {
	file, info, err := openFile(a.files, rel)
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
func (a *App) IssueDownloadURL(user User, rel string) (string, error) {
	return a.issueSignedURL(user, rel, core.SignedURLTTL)
}

// IssueShareLink 给分享链接签一个 ttl 内有效的 URL（10 分钟 / 24 小时两档）。
func (a *App) IssueShareLink(user User, rel string, ttl time.Duration) (string, error) {
	return a.issueSignedURL(user, rel, ttl)
}

// issueSignedURL 确认文件在用户可见范围内后签发 /s/<escaped path>。
//
// 路径始终是数据目录下的全局路径，不按用户重写：这样用户看到的位置就是真实位置，
// 不同用户的同名文件也不会互相撞车。不可见路径按「不存在」处理，不泄露它的存在。
func (a *App) issueSignedURL(user User, rel string, ttl time.Duration) (string, error) {
	if err := validateRelPath(rel); err != nil {
		return "", fileFailure(err)
	}
	if !user.Visible(rel) {
		return "", fileFailure(ErrNotFound)
	}
	if _, _, err := openFile(a.files, rel); err != nil {
		return "", fileFailure(err)
	}
	exp := core.SignedURLExpiry(time.Now(), ttl)
	return a.signurl.SignedURL(escapedDownloadPath(rel), exp), nil
}

// OpenSignedDownload 校验签名 URL 后打开文件：匿名下载没有会话用户，
// 签名覆盖的全局路径就是授权本身。
func (a *App) OpenSignedDownload(rel, method, escapedPath, sig string, exp int64) (*DownloadedFile, error) {
	if err := a.signurl.Verify(method, escapedPath, sig, exp, time.Now()); err != nil {
		if errors.Is(err, core.ErrSignedURLExpired) {
			return nil, core.Fail(http.StatusUnauthorized, "download.expired")
		}
		return nil, core.Fail(http.StatusForbidden, "download.invalid-signature")
	}
	return a.openDownload(rel)
}

// ToggleFavorite 切换当前用户的一个相对路径的收藏状态，返回切换后的状态。
// 只能收藏自己可见的路径：收藏是元数据，但没必要也给不可见路径留记录。
func (a *App) ToggleFavorite(user User, rel string) (bool, error) {
	if err := validateRelPath(rel); err != nil {
		return false, core.Fail(http.StatusBadRequest, "file.invalid-path")
	}
	if !user.Visible(rel) {
		return false, core.Fail(http.StatusNotFound, "file.not-found")
	}
	favorite, err := a.favoritesFor(user).Toggle(rel)
	if err != nil {
		slog.Error("write favorites failed", slog.String("user", user.Name), slog.Any("error", err))
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
