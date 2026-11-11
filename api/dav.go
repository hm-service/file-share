package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"file-share/app"
	"file-share/core"
	"file-share/db"
	"file-share/httpx"
)

// davPrefix 是 WebDAV 的挂载点。它是顶层路由，core 保证分组名不会叫这个。
const davPrefix = "/dav"

// davTicketPath 是 UI 下载入口：票据放在查询参数里，访问日志只记 /davdl。
const davTicketPath = "/davdl"

// DavHandler 返回挂在 /dav 下的 WebDAV 处理器：Basic 认证（用户名随意，密码是 AUTH_TOKEN），
// 文件系统直接读写 dav_node 表与共享 blob 池。
func DavHandler(instance *app.App) http.Handler {
	handler := &webdav.Handler{
		Prefix:     davPrefix,
		FileSystem: davFileSystem{app: instance},
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			// 404 / 403 是客户端的日常，不刷日志；其余错误留一条线索。
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
				slog.Debug("webdav request failed",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("error", err))
			}
		},
	}
	return davAuth{app: instance, cache: newDavTokenCache(), next: davBodyLimit(handler, instance)}
}

// DavDownloadHandler 是「网盘」界面用的票据下载入口：URL 里只有票据，不需要 Basic 凭据。
func DavDownloadHandler(instance *app.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := instance.VerifyDavDownloadTicket(r.URL.Query().Get("t"))
		if err != nil {
			businessFail(w, r, err)
			return
		}
		download, err := instance.DavOpenFile(path)
		if err != nil {
			businessFail(w, r, err)
			return
		}
		defer download.Close()
		serveDownload(w, r, download)
	})
}

// davBodyLimit 给 PUT 加请求体上限：WebDAV 不经过 /api 的 bodyLimit 表，
// 这里按 MAX_FILE_SIZE 直接拒绝。分块上传没有 Content-Length，交给写入时的上限兜底。
func davBodyLimit(next http.Handler, instance *app.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.ContentLength > instance.Config.MaxFileSize {
			http.Error(w, "Payload Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// davAuth 是 WebDAV 的 Basic 认证：WebDAV 客户端基本只支持 Basic，密码就是访问令牌。
// 用户名不参与校验（各家客户端的默认用户名都不一样）。
type davAuth struct {
	app   *app.App
	cache *davTokenCache
	next  http.Handler
}

func (h davAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, password, ok := r.BasicAuth()
	if !ok || !h.cache.valid(password, h.app) {
		w.Header().Set("WWW-Authenticate", `Basic realm="file-share", charset="UTF-8"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	h.next.ServeHTTP(w, r)
}

// davTokenCache 把一次 Argon2id 校验的结果缓存几分钟：挂载端开一个目录就是几十个请求，
// 每个请求都算一次 Argon2id 既慢又毫无意义。键是令牌的 sha256，内存里不留明文令牌。
type davTokenCache struct {
	mu     sync.Mutex
	grants map[string]time.Time
}

const davTokenTTL = 5 * time.Minute

func newDavTokenCache() *davTokenCache {
	return &davTokenCache{grants: make(map[string]time.Time)}
}

func (c *davTokenCache) valid(token string, instance *app.App) bool {
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := time.Now()

	c.mu.Lock()
	expires, found := c.grants[key]
	c.mu.Unlock()
	if found && now.Before(expires) {
		return true
	}
	if !instance.Authenticate(token) {
		return false
	}

	c.mu.Lock()
	// 只该有一个令牌；出现异常增长说明有人在试密码，直接清空，别让缓存变成内存占用。
	if len(c.grants) > 64 {
		c.grants = make(map[string]time.Time)
	}
	c.grants[key] = now.Add(davTokenTTL)
	c.mu.Unlock()
	return true
}

// davFileSystem 是架在 dav_node 表上的 WebDAV 文件系统。
type davFileSystem struct {
	app *app.App
}

func (fs davFileSystem) Mkdir(_ context.Context, name string, _ os.FileMode) error {
	path, err := davInternalPath(name)
	if err != nil {
		return err
	}
	return davOSError(fs.app.DavMkdir(path))
}

func (fs davFileSystem) OpenFile(_ context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	path, err := davInternalPath(name)
	if err != nil {
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_TRUNC) != 0 {
		upload, err := fs.app.DavNewFileUpload(path, -1)
		if err != nil {
			return nil, davOSError(err)
		}
		return &davWriteFile{app: fs.app, path: path, name: davName(path), upload: upload}, nil
	}

	node, err := fs.app.DavStat(path)
	if err != nil {
		return nil, davOSError(err)
	}
	info := davInfo{node: node}
	if node.IsDir {
		return &davDirFile{app: fs.app, info: info}, nil
	}
	download, err := fs.app.DavOpenFile(path)
	if err != nil {
		return nil, davOSError(err)
	}
	return &davReadFile{file: download.Blob, info: info}, nil
}

func (fs davFileSystem) RemoveAll(_ context.Context, name string) error {
	path, err := davInternalPath(name)
	if err != nil {
		return err
	}
	return davOSError(fs.app.DavDelete(path))
}

func (fs davFileSystem) Rename(_ context.Context, oldName, newName string) error {
	oldPath, err := davInternalPath(oldName)
	if err != nil {
		return err
	}
	newPath, err := davInternalPath(newName)
	if err != nil {
		return err
	}
	return davOSError(fs.app.DavMove(oldPath, newPath))
}

func (fs davFileSystem) Stat(_ context.Context, name string) (os.FileInfo, error) {
	path, err := davInternalPath(name)
	if err != nil {
		return nil, err
	}
	node, err := fs.app.DavStat(path)
	if err != nil {
		return nil, davOSError(err)
	}
	return davInfo{node: node}, nil
}

// davInternalPath 把 WebDAV 请求路径（"/a/b"、"/"）转成内部路径（"a/b"、""）。
func davInternalPath(name string) (string, error) {
	path, ok := core.NormalizeDavPath(name)
	if !ok {
		return "", os.ErrNotExist
	}
	return path, nil
}

// davName 取内部路径的最后一段；根返回 "/"。
func davName(path string) string {
	if path == "" {
		return "/"
	}
	_, name := core.SplitPath(path)
	return name
}

// davOSError 把业务错误翻译成 WebDAV 处理器认识的 os 错误。
func davOSError(err error) error {
	if err == nil {
		return nil
	}
	var business *core.Error
	if errors.As(err, &business) {
		switch business.Status {
		case http.StatusNotFound:
			return os.ErrNotExist
		case http.StatusConflict:
			return os.ErrExist
		case http.StatusBadRequest, http.StatusForbidden:
			return os.ErrPermission
		case http.StatusRequestEntityTooLarge:
			return errDavTooBig
		}
	}
	return err
}

var errDavTooBig = errors.New("dav: file too large")

// davInfo 是 os.FileInfo 的 WebDAV 版：额外提供 ETag（文件的 sha256），
// 让挂载端能在内容变化时正确失效缓存。
type davInfo struct {
	node *db.DavNode
}

func (i davInfo) Name() string  { return i.node.Name }
func (i davInfo) Size() int64   { return i.node.Size }
func (i davInfo) IsDir() bool   { return i.node.IsDir }
func (i davInfo) Sys() any      { return nil }
func (i davInfo) Mode() os.FileMode {
	if i.node.IsDir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (i davInfo) ModTime() time.Time { return time.Unix(i.node.UpdatedAt, 0) }

// ETag 给文件返回内容摘要，目录返回修改时间：两者都不需要额外存储。
func (i davInfo) ETag(context.Context) (string, error) {
	if i.node.IsDir {
		return fmt.Sprintf(`"d%x"`, i.node.UpdatedAt), nil
	}
	return `"` + i.node.SHA256 + `"`, nil
}

// davReadFile 是只读文件：直接包住 blob 的 *os.File，Seek / Range 都由它负责。
type davReadFile struct {
	file *os.File
	info davInfo
}

func (f *davReadFile) Close() error                            { return f.file.Close() }
func (f *davReadFile) Read(p []byte) (int, error)              { return f.file.Read(p) }
func (f *davReadFile) Seek(offset int64, whence int) (int64, error) { return f.file.Seek(offset, whence) }
func (f *davReadFile) Readdir(int) ([]os.FileInfo, error)      { return nil, fs.ErrInvalid }
func (f *davReadFile) Stat() (os.FileInfo, error)              { return f.info, nil }
func (f *davReadFile) Write([]byte) (int, error)               { return 0, os.ErrPermission }

// davDirFile 是目录：Readdir 时按需查一次子节点。
type davDirFile struct {
	app     *app.App
	info    davInfo
	entries []os.FileInfo
	loaded  bool
}

func (f *davDirFile) Close() error                       { return nil }
func (f *davDirFile) Read([]byte) (int, error)           { return 0, fs.ErrInvalid }
func (f *davDirFile) Seek(int64, int) (int64, error)     { return 0, fs.ErrInvalid }
func (f *davDirFile) Write([]byte) (int, error)          { return 0, os.ErrPermission }
func (f *davDirFile) Stat() (os.FileInfo, error)         { return f.info, nil }

func (f *davDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if !f.loaded {
		nodes, err := f.app.DavList(f.info.node.Path)
		if err != nil {
			return nil, davOSError(err)
		}
		f.entries = make([]os.FileInfo, 0, len(nodes))
		for index := range nodes {
			f.entries = append(f.entries, davInfo{node: &nodes[index]})
		}
		f.loaded = true
	}
	if count <= 0 {
		entries := f.entries
		f.entries = nil
		return entries, nil
	}
	if len(f.entries) == 0 {
		return nil, io.EOF
	}
	if count > len(f.entries) {
		count = len(f.entries)
	}
	entries := f.entries[:count]
	f.entries = f.entries[count:]
	return entries, nil
}

// davWriteFile 是 PUT / COPY 的落点：先写临时文件，Close 时才算 sha、落 blob、写记录。
// 上传中途失败（例如超过体积上限）就把临时文件丢掉，不留下半截数据。
type davWriteFile struct {
	app    *app.App
	path   string
	name   string
	upload *app.Upload
	failed bool
}

func (f *davWriteFile) Write(p []byte) (int, error) {
	n, err := f.upload.Write(p)
	if err != nil {
		f.failed = true
	}
	return n, err
}

// Stat 在 Close 之前被调用（处理器要取 ETag），这里返回尚未提交的估算值。
func (f *davWriteFile) Stat() (os.FileInfo, error) {
	return davInfo{node: &db.DavNode{
		Path:      f.path,
		Name:      f.name,
		SHA256:    f.upload.Sum(),
		Size:      f.upload.Size(),
		UpdatedAt: time.Now().Unix(),
	}}, nil
}

func (f *davWriteFile) Close() error {
	if f.upload == nil {
		return nil
	}
	upload := f.upload
	f.upload = nil
	if f.failed {
		upload.Abort()
		return nil
	}
	_, err := f.app.DavSaveUpload(f.path, upload)
	return davOSError(err)
}

func (f *davWriteFile) Read([]byte) (int, error)       { return 0, os.ErrPermission }
func (f *davWriteFile) Seek(int64, int) (int64, error) { return 0, os.ErrPermission }
func (f *davWriteFile) Readdir(int) ([]os.FileInfo, error) {
	return nil, fs.ErrInvalid
}

// handleDavList 是「网盘」界面的目录列表接口：返回当前目录的直接子节点。
func (s *Server) handleDavList(w http.ResponseWriter, r *http.Request) {
	path, ok := core.NormalizeDavPath(r.URL.Query().Get("path"))
	if !ok {
		fail(w, r, http.StatusBadRequest, "dav.invalid")
		return
	}
	entries, err := s.App.DavList(path)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"path": path, "entries": entries})
}

// handleDavTicket 给「网盘」界面签发一个短期下载地址：浏览器直接导航下载，不需要 Basic 凭据。
func (s *Server) handleDavTicket(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	path, ok := core.NormalizeDavPath(payload.Path)
	if !ok || path == "" {
		fail(w, r, http.StatusBadRequest, "dav.invalid")
		return
	}
	ticket, err := s.App.IssueDavDownloadTicket(path)
	if err != nil {
		businessFail(w, r, err)
		return
	}
	httpx.Write(w, http.StatusOK, map[string]any{"url": davTicketPath + "?t=" + ticket})
}