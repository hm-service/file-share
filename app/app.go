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
	URLSignatureKey   string
	AuthTokenHash     string
	ReadyzToken       string
	MaxFileSize       int64
	// S3 为 nil 时 blob 存在本地 DATA_DIR/blobs 下，否则存在 S3。
	S3 *S3Config
}

// DATA_DIR 下的固定布局：blob 仓库与上传暂存分开，库文件放根目录。
const (
	blobsDirName = "blobs"
	tempDirName  = "temp"
	dbFileName   = "file-share.db"
)

// StorageInfo 描述当前 blob 的存放方案：本地目录或 S3 对象存储。
// 只含可公开的信息（后端类型、桶名、前缀），不含 endpoint 与凭据；
// 启动日志与 /api/storage 共用它，界面据此提示当前存储方案。
type StorageInfo struct {
	Backend string `json:"backend"` // local 或 s3
	Bucket  string `json:"bucket,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
}

// Describe 是日志里的一行描述：s3://<bucket>/<prefix> 或 local。
func (s StorageInfo) Describe() string {
	if s.Backend == "s3" {
		return "s3://" + s.Bucket + "/" + s.Prefix
	}
	return s.Backend
}

// App 持有全部跨请求状态。
type App struct {
	Config  Config
	Store   *db.Store
	Blobs   BlobStore
	Storage StorageInfo

	blobsDir string
	tempDir  string
	dbPath   string
	signer   *core.SessionSigner
	signurl  *core.URLSigner
	proxies  *httpx.Proxies

	// mu 把「插入记录 / 改分组 / 批量排序」这些 DB 写操作串起来，
	// 保证插入时看到的当前最大 sort_order 与分组状态是稳定的。
	// 它不保护 blob：blob 的生命周期由 shaLocks 按 sha 分片串行，
	// 这样上传/下载的网络 IO 不会把全站写操作堵住。
	mu sync.Mutex

	// shaLocks 按 sha 分片串行同一份内容的「判断存在 / 落位 / 删除 + 引用计数」，
	// 防止一个删除把另一条记录刚引用的 blob 删掉；不同 sha 之间互不阻塞。
	shaLocks [64]sync.Mutex
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
	if err := moveLegacyBlobDir(cfg.DataDir); err != nil {
		return nil, err
	}
	blobsDir := filepath.Join(cfg.DataDir, blobsDirName)
	if err := ensureDir(blobsDir, "blobs"); err != nil {
		return nil, err
	}
	tempDir := filepath.Join(cfg.DataDir, tempDirName)
	if err := ensureDir(tempDir, "upload temp"); err != nil {
		return nil, err
	}
	if cfg.S3 != nil && (cfg.S3.Endpoint == "" || cfg.S3.Bucket == "") {
		return nil, fmt.Errorf("S3 config requires endpoint and bucket")
	}
	proxies, err := httpx.NewProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, dbFileName)
	store, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// 残留的上传临时文件是上次进程异常退出的痕迹，留着只会白占空间。
	// blobs/ 里的 .upload-* 只可能是旧布局（临时文件曾与 blob 同目录）留下的。
	for _, dir := range []string{tempDir, blobsDir} {
		if err := removeLeftoverUploads(dir); err != nil {
			store.Close()
			return nil, fmt.Errorf("clean upload temp files in %s: %w", dir, err)
		}
	}
	var blobs BlobStore
	if cfg.S3 != nil {
		s3, err := newS3BlobStore(*cfg.S3)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("init s3 blob store: %w", err)
		}
		blobs = s3
	} else {
		// blob 的权限可能是旧版本或手工操作留下的，启动时统一收紧。
		if err := tightenBlobs(blobsDir); err != nil {
			store.Close()
			return nil, fmt.Errorf("tighten blob permissions: %w", err)
		}
		blobs = newLocalBlobStore(blobsDir)
	}
	storage := StorageInfo{Backend: blobs.Kind()}
	if cfg.S3 != nil {
		storage.Bucket = cfg.S3.Bucket
		storage.Prefix = strings.Trim(cfg.S3.Prefix, "/")
	}
	return &App{
		Config:   cfg,
		Store:    store,
		Blobs:    blobs,
		Storage:  storage,
		blobsDir: blobsDir,
		tempDir:  tempDir,
		dbPath:   dbPath,
		signer:   core.NewSessionSigner([]byte(cfg.SessionSigningKey)),
		signurl:  core.NewURLSigner([]byte(cfg.URLSignatureKey)),
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

// ensureDir 建出 DATA_DIR 下的固定子目录并收紧权限。子目录由应用自己维护，
// 缺了就是新部署，建出来再继续；建不出来说明卷或权限有问题，当场失败。
func ensureDir(path, label string) error {
	if err := os.MkdirAll(path, core.DataDirPerm); err != nil {
		return fmt.Errorf("create %s dir: %w", label, err)
	}
	if err := core.TightenPerm(path, core.DataDirPerm); err != nil {
		return fmt.Errorf("tighten %s dir: %w", label, err)
	}
	return nil
}

// moveLegacyBlobDir 把旧布局的 blob 目录 DATA_DIR/files 改名为 DATA_DIR/blobs。
//
// 旧版本把 blob 与上传临时文件都放在 files/ 下，新版本拆成 blobs/ 与 temp/。
// 改名只动目录名、不搬内容（同一文件系统内是一次原子 rename）。两个目录同时存在
// 说明有人手工动过数据，直接报错让人确认，不猜哪份才是真的。
func moveLegacyBlobDir(dataDir string) error {
	legacy := filepath.Join(dataDir, "files")
	info, err := os.Stat(legacy)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("stat legacy blob dir %s: %w", legacy, err)
	case !info.IsDir():
		return fmt.Errorf("legacy path %s is not a directory", legacy)
	}
	target := filepath.Join(dataDir, blobsDirName)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("both %s and %s exist; remove one before starting", legacy, target)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", target, err)
	}
	if err := os.Rename(legacy, target); err != nil {
		return fmt.Errorf("rename %s to %s: %w", legacy, target, err)
	}
	slog.Info("moved legacy blob dir", slog.String("from", legacy), slog.String("to", target))
	return nil
}

func removeLeftoverUploads(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".upload-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func tightenBlobs(blobsDir string) error {
	entries, err := os.ReadDir(blobsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// 不跟随符号链接：收紧权限只该作用在数据目录自己的文件上。
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(blobsDir, entry.Name())
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
	if err := core.CheckPerm(a.blobsDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("blobs dir: %w", err)
	}
	if err := core.CheckPerm(a.tempDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("temp dir: %w", err)
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

// shaMutex 按 sha 取一把分片锁：同一份内容的存在性判断、落位与删除互斥，
// 不同内容之间不互相阻塞。sha 正常是 64 位十六进制，取前 2 位当分片下标；
// 名字再短也照样落到某个分片 —— 绝不能退回全局锁 a.mu：它在 DeleteFile 里还会再锁一次，
// 而 sync.Mutex 不可重入，同一个 goroutine 连加两次就是永久自死锁。
func (a *App) shaMutex(sha string) *sync.Mutex {
	var index int
	for i := 0; i < 2 && i < len(sha); i++ {
		index = index<<4 | hexNibble(sha[i])
	}
	return &a.shaLocks[index&(len(a.shaLocks)-1)]
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		// sha 一定是小写十六进制；意外字符按 0 处理，最坏是退化成共享分片。
		return 0
	}
}
