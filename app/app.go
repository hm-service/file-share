// Package app 是用例层：打开 / 关闭只读视图，扫描数据目录，签发下载与分享 URL。
// 只读视图不落任何文件副本，HTTP 细节不在这里。
package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"file-share/core"
	"file-share/httpx"
)

// Config 是应用容器的启动配置，全部由 main.go 从环境变量读出并校验后传入。
type Config struct {
	// DataDir 是可写的数据目录：用户表与收藏元数据；容器里由卷挂载进来。
	DataDir string
	// FilesDir 是只读的文件目录：SMB 共享的数据根（部署时只读挂载）。
	FilesDir          string
	TrustedProxies    string
	SessionSigningKey string
	URLSignatureKey   string
	ReadyzToken       string
	// Users 是登录用户表，由 main.go 从 DATA_DIR/users.yaml 读入。
	Users []User
}

// App 持有全部跨请求状态。
type App struct {
	Config Config

	// files 是只读数据目录的根句柄：所有文件访问都经它，天然限制在数据目录内。
	files *os.Root

	// users 按用户名索引的只读用户表：每次请求按当前表解析权限与可见范围。
	users map[string]User
	// favorites 是每用户独立的收藏集合：DATA_DIR/favorites/<name>.json。
	favorites map[string]*Favorites

	signer  *core.SessionSigner
	signurl *core.URLSigner
	proxies *httpx.Proxies
}

// Open 初始化只读视图与 DATA_DIR 下的状态。所有启动一次性工作都在这里做，
// 失败就直接返回错误让进程退出，不留下半启动的服务。
func Open(cfg Config) (*App, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("DATA_DIR is required")
	}
	if cfg.FilesDir == "" {
		return nil, errors.New("FILES_DIR is required")
	}
	if err := prepareDataDir(cfg.DataDir); err != nil {
		return nil, err
	}
	if err := checkFilesDir(cfg.FilesDir); err != nil {
		return nil, err
	}
	users, err := indexUsers(cfg.Users)
	if err != nil {
		return nil, err
	}
	files, err := os.OpenRoot(cfg.FilesDir)
	if err != nil {
		return nil, fmt.Errorf("open FILES_DIR %q: %w", cfg.FilesDir, err)
	}
	a := &App{
		Config:    cfg,
		files:     files,
		users:     users,
		favorites: make(map[string]*Favorites, len(users)),
	}
	opened := false
	defer func() {
		if !opened {
			a.Close()
		}
	}()
	// 可见范围是启动时的硬约束：逐条解析成文件系统的真实大小写路径。
	for name, user := range users {
		resolved := make([]string, 0, len(user.Access))
		seen := make(map[string]bool, len(user.Access))
		for _, access := range user.Access {
			real, err := resolveAccess(files, access)
			if err != nil {
				return nil, fmt.Errorf("user %q: %w", name, err)
			}
			if seen[real] {
				return nil, fmt.Errorf("user %q: access %q resolves to a duplicate directory %q", name, access, real)
			}
			seen[real] = true
			resolved = append(resolved, real)
		}
		user.Access = resolved
		users[name] = user
	}
	// 收藏按用户分文件：目录先建出来，再逐用户读入（新用户没有文件就是空收藏）。
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, favoritesDirName), core.DataDirPerm); err != nil {
		return nil, fmt.Errorf("create favorites dir: %w", err)
	}
	for name := range users {
		favorites, err := openFavorites(cfg.DataDir, name)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", name, err)
		}
		a.favorites[name] = favorites
	}
	proxies, err := httpx.NewProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	a.proxies = proxies
	a.signer = core.NewSessionSigner([]byte(cfg.SessionSigningKey))
	a.signurl = core.NewURLSigner([]byte(cfg.URLSignatureKey))
	opened = true
	return a, nil
}

// prepareDataDir 只认已经存在的数据目录：目录由部署方交进来（卷挂载、手工建），
// 缺了就当场失败，不顺手建一个把路径写错的部署掩盖过去。确认存在后收紧到 0700，
// 再写一个探针文件确认真的可写 —— 别等第一次写收藏才发现。
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

// checkFilesDir 确认只读数据目录存在、是目录且当前进程可读。
// 不检查可写：部署时它是只读挂载，可写反而是配置错误 —— 由部署清单与卷挂载保证。
func checkFilesDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("FILES_DIR %s does not exist; mount the read-only data volume there", dir)
	case err != nil:
		return fmt.Errorf("stat FILES_DIR: %w", err)
	case !info.IsDir():
		return fmt.Errorf("FILES_DIR %s is not a directory", dir)
	}
	if _, err := os.ReadDir(dir); err != nil {
		return fmt.Errorf("FILES_DIR %s is not readable: %w", dir, err)
	}
	return nil
}

// Close 关闭只读目录句柄。
func (a *App) Close() error {
	if a.files == nil {
		return nil
	}
	return a.files.Close()
}

// Ready 是 /readyz 的探针：DATA_DIR 权限与可写性、只读数据目录可读、
// 收藏元数据可读，全部满足才算就绪。
func (a *App) Ready() error {
	if err := core.CheckPerm(a.Config.DataDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if _, err := fs.Stat(a.files.FS(), "."); err != nil {
		return fmt.Errorf("files dir: %w", err)
	}
	if _, err := os.ReadDir(a.Config.FilesDir); err != nil {
		return fmt.Errorf("files dir not readable: %w", err)
	}
	favoritesDir := filepath.Join(a.Config.DataDir, favoritesDirName)
	if err := core.CheckPerm(favoritesDir, core.DataDirPerm); err != nil {
		return fmt.Errorf("favorites dir: %w", err)
	}
	for name, favorites := range a.favorites {
		if _, err := os.Stat(favorites.path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("user %q: stat favorites file: %w", name, err)
		}
		if err := core.CheckPerm(favorites.path, core.SecretPerm); err != nil {
			return fmt.Errorf("user %q: favorites file: %w", name, err)
		}
	}
	// 收藏切换要写 DATA_DIR：启动后再探一次，卷变成只读时能尽早发现。
	probe := filepath.Join(a.Config.DataDir, ".ready")
	if err := os.WriteFile(probe, []byte("ok"), core.SecretPerm); err != nil {
		return fmt.Errorf("data dir not writable: %w", err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("remove data dir probe: %w", err)
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

// favoritesFor 返回该用户的收藏集合。
func (a *App) favoritesFor(user User) *Favorites { return a.favorites[user.Name] }

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
		access, err := normalizeAccess(user.Access)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", user.Name, err)
		}
		user.Access = access
		index[user.Name] = user
	}
	return index, nil
}
