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
	ReadyzToken       string
	MaxFileSize       int64
	// Users 是登录用户表，由 main.go 从 DATA_DIR/users.yaml 读入。
	Users []User
	// S3 为 nil 时 blob 存在本地 DATA_DIR/blobs 下，否则存在 S3。
	S3 *S3Config
}

// DATA_DIR 下的固定布局：blob 仓库与上传暂存分开，库文件放根目录。
const (
	blobsDirName = "blobs"
	tempDirName  = "temp"
	dbFileName   = "file-share.db"
)

// legacyBlobDirName 是旧版本存 blob 的目录名。当前只认 blobs/，不做改名兼容，
// 只在检测到旧布局时提醒迁移 —— 否则升级后列表照常、下载却全部 404，没人知道为什么。
const legacyBlobDirName = "files"

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
	// users 按用户名索引的只读用户表：每次请求按当前表解析权限。
	users   map[string]User
	signer  *core.SessionSigner
	signurl *core.URLSigner
	proxies *httpx.Proxies

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
	users, err := indexUsers(cfg.Users)
	if err != nil {
		return nil, err
	}
	blobsDir := filepath.Join(cfg.DataDir, blobsDirName)
	if err := ensureDir(blobsDir, "blobs"); err != nil {
		return nil, err
	}
	warnLegacyBlobDir(cfg.DataDir, blobsDir)
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
	if err := removeLeftoverUploads(tempDir); err != nil {
		store.Close()
		return nil, fmt.Errorf("clean upload temp files: %w", err)
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
		users:    users,
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

// warnLegacyBlobDir 在 blobs/ 为空而旧的 files/ 有内容时提醒迁移。只打日志：
// 自动改名与拒绝启动都属于反向兼容，当前版本明确不做。
func warnLegacyBlobDir(dataDir, blobsDir string) {
	legacy := filepath.Join(dataDir, legacyBlobDirName)
	if !dirHasEntries(legacy) || dirHasEntries(blobsDir) {
		return
	}
	slog.Warn("legacy blob directory found",
		slog.String("legacy_dir", legacy),
		slog.String("blobs_dir", blobsDir),
		slog.String("hint", "old versions stored blobs under files/; stop the service and run `mv files blobs` inside DATA_DIR, otherwise existing files will 404"),
	)
}

// dirHasEntries 报告目录存在且至少有一个条目。
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
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

// Authenticate 校验用户名与密码，恒定至少花 1s + 抖动才返回：
// 对错都等这么久，抹掉「密码不对就秒回」这个时序侧信道。
func (a *App) Authenticate(username, password string) bool {
	start := time.Now()
	user, found := a.users[username]
	// 用户不存在时也拿一个固定摘要比较，避免用耗时区分「用户不存在」与「密码不对」。
	want := sha256.Sum256([]byte(user.Password))
	got := sha256.Sum256([]byte(password))
	ok := subtle.ConstantTimeCompare(got[:], want[:]) == 1
	core.SleepUntil(start, core.LoginFloor())
	return found && ok
}

// IssueSession 给指定用户签一张会话凭据，有效期 30 天。
func (a *App) IssueSession(username string) (token string, expiresAt int64, err error) {
	return a.signer.Issue(time.Now(), username)
}

// VerifySession 校验会话凭据并返回对应用户；签名不对 / 过期分别返回
// core.ErrSessionMalformed / ErrSessionExpired。用户已从用户表移除时也按凭据无效处理。
func (a *App) VerifySession(token string) (User, error) {
	_, username, err := a.signer.Verify(token, time.Now())
	if err != nil {
		return User{}, err
	}
	user, found := a.users[username]
	if !found {
		return User{}, core.ErrSessionMalformed
	}
	return user, nil
}

// User 按用户名取当前用户表里的一项；登录响应与界面初始化要拿权限。
func (a *App) User(name string) (User, bool) {
	user, ok := a.users[name]
	return user, ok
}

// indexUsers 校验用户表并建索引。空表说明部署漏了用户表文件，直接拒绝启动。
func indexUsers(users []User) (map[string]User, error) {
	if len(users) == 0 {
		return nil, errors.New("at least one user is required")
	}
	index := make(map[string]User, len(users))
	for _, user := range users {
		if user.Name == "" {
			return nil, errors.New("user name must not be empty")
		}
		if _, exists := index[user.Name]; exists {
			return nil, fmt.Errorf("duplicate user name %q", user.Name)
		}
		if user.Permissions == 0 {
			return nil, fmt.Errorf("user %q has no permissions", user.Name)
		}
		index[user.Name] = user
	}
	return index, nil
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
