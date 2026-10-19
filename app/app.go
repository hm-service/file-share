// Package app 是用例层：打开 / 关闭应用容器，编排文件落盘，并把启动时的一次性工作
// （建表、收紧权限、清理残留临时文件、旧格式检测）做完。HTTP 细节不在这里。
package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"file-share/core"
	"file-share/db"
	"file-share/httpx"
)

// Config 是应用容器的启动配置，全部由 main.go 从环境变量读出并校验后传入。
type Config struct {
	DataDir           string
	TrustedProxies    string
	SessionSigningKey string
	AuthTokenHash     string
	ReadyzToken       string
	MaxFileSize       int64
}

// App 持有全部跨请求状态。
type App struct {
	Config Config
	Store  *db.Store

	filesDir string
	dbPath   string
	signer   *core.SessionSigner
	proxies  *httpx.Proxies

	// mu 把「check_file 落记录 / 上传落 blob 与记录 / 删除记录与 blob」串起来：
	// 引用计数与 blob 删除之间若插进别的写操作，就会删掉刚被引用的 blob。
	mu sync.Mutex
}

// Open 初始化 DATA_DIR 下的全部状态。所有启动一次性工作都在这里做，
// 失败就直接返回错误让进程退出，不留下半启动的服务。
func Open(cfg Config) (*App, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("DATA_DIR is required")
	}
	if cfg.MaxFileSize < 1 {
		return nil, errors.New("MAX_FILE_SIZE must be >= 1")
	}
	if err := prepareDataDir(cfg.DataDir); err != nil {
		return nil, err
	}
	filesDir := filepath.Join(cfg.DataDir, "files")
	if err := os.MkdirAll(filesDir, core.DataDirPerm); err != nil {
		return nil, fmt.Errorf("create files dir: %w", err)
	}
	if err := core.TightenPerm(filesDir, core.DataDirPerm); err != nil {
		return nil, fmt.Errorf("tighten files dir: %w", err)
	}
	proxies, err := httpx.NewProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, "file-share.db")
	store, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// 残留的上传临时文件是上次进程异常退出的痕迹，留着只会白占空间。
	if err := removeLeftoverUploads(filesDir); err != nil {
		store.Close()
		return nil, fmt.Errorf("clean upload temp files: %w", err)
	}
	// blob 的权限可能是旧版本或手工操作留下的，启动时统一收紧。
	if err := tightenBlobs(filesDir); err != nil {
		store.Close()
		return nil, fmt.Errorf("tighten blob permissions: %w", err)
	}
	return &App{
		Config:   cfg,
		Store:    store,
		filesDir: filesDir,
		dbPath:   dbPath,
		signer:   core.NewSessionSigner([]byte(cfg.SessionSigningKey)),
		proxies:  proxies,
	}, nil
}

// prepareDataDir 只认已经存在的数据目录：目录由部署方交进来（卷挂载、手工建），
// 缺了就当场失败，不顺手建一个把路径写错的部署掩盖过去。确认存在后收紧到 0700，
// 再写一个探针文件确认真的可写 —— 别等第一次落盘才发现。
// 动手之前先拦下旧版数据：旧版把状态放在 files.db 与 favorite.json 里，不迁移。
func prepareDataDir(dataDir string) error {
	info, err := os.Stat(dataDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("DATA_DIR %s does not exist; create it or mount a volume there", dataDir)
	case err != nil:
		return fmt.Errorf("stat DATA_DIR: %w", err)
	case !info.IsDir():
		return fmt.Errorf("DATA_DIR %s is not a directory", dataDir)
	}
	for _, legacy := range []string{"files.db", "favorite.json"} {
		_, statErr := os.Stat(filepath.Join(dataDir, legacy))
		if statErr == nil {
			return fmt.Errorf("legacy data file %s found; clear DATA_DIR %s and start over", legacy, dataDir)
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("stat legacy data file %s: %w", legacy, statErr)
		}
	}
	if err := core.TightenPerm(dataDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("tighten DATA_DIR: %w", err)
	}
	probe := filepath.Join(dataDir, ".startup")
	if err := os.WriteFile(probe, []byte("ok"), core.SecretPerm); err != nil {
		return fmt.Errorf("DATA_DIR %s is not writable: %w", dataDir, err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("remove DATA_DIR probe: %w", err)
	}
	return nil
}

func removeLeftoverUploads(filesDir string) error {
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".upload-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if err := os.Remove(filepath.Join(filesDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func tightenBlobs(filesDir string) error {
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// 不跟随符号链接：收紧权限只该作用在数据目录自己的文件上。
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(filesDir, entry.Name())
		mode := fs.FileMode(core.SecretPerm)
		if entry.IsDir() {
			mode = core.DataDirPerm
		}
		if err := core.TightenPerm(path, mode); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭数据库；错误交给调用方 —— 关闭失败要在退出码里体现。
func (a *App) Close() error {
	if a.Store == nil {
		return nil
	}
	return a.Store.Close()
}

// Ready 是 /readyz 的探针：目录与库文件权限、数据库可读可写，全部满足才算就绪。
func (a *App) Ready() error {
	if err := core.CheckPerm(a.Config.DataDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if err := core.CheckPerm(a.filesDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("files dir: %w", err)
	}
	if err := core.CheckPerm(a.dbPath, core.SecretPerm); err != nil {
		return fmt.Errorf("database file: %w", err)
	}
	if err := a.Store.Ready(); err != nil {
		return fmt.Errorf("database not readable/writable: %w", err)
	}
	return nil
}

// ReadyAuthorized 判断这次 /readyz 请求该不该应答：配了 READYZ_TOKEN 就要求 ?token= 与它一致。
//
// 两边先各自 sha256 再恒时比较：subtle.ConstantTimeCompare 在长度不等时会立刻返回 0，
// 直接比原文就把「长度对不对」漏成了时间差。
func (a *App) ReadyAuthorized(token string) bool {
	expected := a.Config.ReadyzToken
	if expected == "" {
		return true
	}
	got := sha256.Sum256([]byte(token))
	want := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// ClientIP 按可信代理设置取客户端 IP。
func (a *App) ClientIP(r *http.Request) string { return a.proxies.ClientIP(r) }

// Authenticate 校验登录令牌，恒定至少花 1s + 抖动才返回：
// token 对不对都等这么久，抹掉「错令牌秒回」这个时序侧信道。
func (a *App) Authenticate(token string) bool {
	start := time.Now()
	ok, err := core.VerifyToken(token, a.Config.AuthTokenHash)
	if err != nil {
		// 配置里的哈希串坏了（启动逻辑的 bug）：只记错误，按校验失败处理。
		slog.Error("auth token hash is invalid", slog.Any("error", err))
		ok = false
	}
	core.SleepUntil(start, core.LoginFloor())
	return ok
}

// IssueSession 签一张会话凭据，有效期 30 天。
func (a *App) IssueSession() (token string, expiresAt int64, err error) {
	return a.signer.Issue(time.Now())
}

// VerifySession 校验会话凭据；签名不对 / 过期分别返回 core.ErrSessionMalformed / ErrSessionExpired。
func (a *App) VerifySession(token string) error {
	_, err := a.signer.Verify(token, time.Now())
	return err
}

func (a *App) blobPath(sha string) string { return filepath.Join(a.filesDir, sha) }
