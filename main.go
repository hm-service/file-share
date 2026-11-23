package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"file-share/api"
	"file-share/app"
)

// shutdownTimeout 是优雅关闭的时限：等在手请求最多等这么久，超时就强制关掉，
// 进程以 254 退出。
const shutdownTimeout = 5 * time.Second

// defaultDataDir 是未设置 DATA_DIR 时的默认数据目录：容器部署时 /data 由卷挂载进来。
const defaultDataDir = "/data"

// defaultFilesDir 是未设置 FILES_DIR 时的默认只读数据目录：
// 容器部署时把只读数据卷挂到 /data/files（SMB 共享的数据根）。
const defaultFilesDir = "/data/files"

// usersFileName 是数据目录下的用户表文件名：用户表固定读 DATA_DIR/users.yaml，
// 与收藏元数据同级；容器部署时由 Secret 挂成这个文件。
const usersFileName = "users.yaml"

// initLogger 把默认 logger 换成输出到 stdout 的 JSON handler，
// 时间字段按约定叫 timestamp。
func initLogger() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				attr.Key = "timestamp"
			}
			return attr
		},
	})))
}

// concurrencyLimits 是三个独立闸门的槽位数：认证、其余接口、静态文件。
type concurrencyLimits struct {
	auth, api, static int
}

type config struct {
	app  app.Config
	host string
	port int
}

func main() {
	os.Exit(run())
}

func run() int {
	initLogger()

	cfg, limits, err := loadConfig()
	if err != nil {
		slog.Error("init failed", slog.Any("error", err))
		return 1
	}
	instance, err := app.Open(cfg.app)
	if err != nil {
		slog.Error("init failed", slog.Any("error", err))
		return 1
	}
	// 自己 Listen 而不是 ListenAndServe：先占住端口再打启动日志，端口写 0 时也只有这里看得到实际端口。
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)))
	if err != nil {
		slog.Error("init failed", slog.Any("error", err))
		instance.Close()
		return 1
	}
	server := &http.Server{
		Handler:           newRootHandler(instance, Dist, limits),
		ReadHeaderTimeout: 10 * time.Second,
	}
	userAccess := make(map[string][]string, len(instance.Config.Users))
	for _, user := range instance.Config.Users {
		userAccess[user.Name] = user.Access
	}
	slog.Info("server started",
		slog.String("addr", listener.Addr().String()),
		slog.String("data_dir", instance.Config.DataDir),
		slog.String("files_dir", instance.Config.FilesDir),
		slog.Any("user_access", userAccess),
		slog.String("start_at", time.Now().Format(time.RFC3339)),
	)
	return serve(server, listener, instance)
}

// newRootHandler 把两半接起来：站点外壳（web.go）管页面与静态资源，api 包管接口、探针与
// 签名下载；接口按路由组各套一层独立闸门，外面再套共用的中间件（访问日志、安全头、语言协商）。
func newRootHandler(instance *app.App, files fs.FS, limits concurrencyLimits) http.Handler {
	routes := api.NewMux(instance)

	// 签名下载（/s/{id}）与页面 / 静态资源同属「静态」一类，共用一个信号量。
	static := http.NewServeMux()
	static.Handle("/s", routes)
	static.Handle("/s/", routes)
	static.Handle("/", siteHandler(files))
	staticHandler := api.Concurrency(limits.static, static)

	root := http.NewServeMux()
	// 认证单独一组：它被打满不该把业务请求挤在后面；/readyz 自己一组、只给几个槽位；
	// /livez 不设闸门 —— 只要进程活着就必须能回 200。落在 /api/ 下的新接口自动被认领。
	root.Handle("/api/auth", api.Concurrency(limits.auth, routes))
	root.Handle("/api/", api.Concurrency(limits.api, routes))
	root.Handle("/readyz", api.Concurrency(api.ReadyzConcurrency, routes))
	root.Handle("/livez", routes)
	root.Handle("/s", staticHandler)
	root.Handle("/s/", staticHandler)
	root.Handle("/", staticHandler)
	return api.Middleware(instance, root)
}

// serve 跑起服务并处理停止信号：第一次 SIGINT / SIGTERM 走优雅关闭，
// 之后的信号交回默认处置 —— 卡在等待里时，再按一次 Ctrl-C 就该立刻结束。
//
// 返回值即进程退出码：正常关闭 0；Shutdown 超时被强制关闭 254；关库失败 255。
func serve(server *http.Server, listener net.Listener, instance *app.App) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	var received os.Signal
	select {
	case received = <-signals:
		signal.Stop(signals)
		slog.Info("shutdown signal received", slog.String("signal", signalName(received)))
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server exited with error", slog.Any("error", err))
			instance.Close()
			return 1
		}
		instance.Close()
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown timed out", slog.Any("error", err))
		server.Close()
		instance.Close()
		return 254
	}
	if err := instance.Close(); err != nil {
		slog.Error("closing data store failed", slog.Any("error", err))
		return 255
	}
	slog.Info("server shutdown",
		slog.String("signal", signalName(received)),
		slog.String("stop_at", time.Now().Format(time.RFC3339)),
	)
	return 0
}

// signalName 把信号写成 SIGINT / SIGTERM 这样的规范名：Go 默认的 "interrupt" / "terminated"
// 在运维侧不直观。
func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return sig.String()
}

// loadConfig 读并校验全部环境变量与 Secret；任何一项写错都让进程起不来。
func loadConfig() (config, concurrencyLimits, error) {
	var (
		zero   config
		limits concurrencyLimits
	)
	if err := checkTimezone(); err != nil {
		return zero, limits, err
	}
	host, err := readHost()
	if err != nil {
		return zero, limits, err
	}
	port, err := readPort()
	if err != nil {
		return zero, limits, err
	}
	if limits, err = readConcurrencyLimits(); err != nil {
		return zero, limits, err
	}
	dataDir := readDataDir()
	filesDir := readFilesDir()
	// 用户表就在 DATA_DIR 下：先确认目录存在，别让「读不到 users.yaml」把
	// 卷没挂上 / 路径写错这个真正原因盖住。可写性由 app.Open 统一校验。
	info, err := os.Stat(dataDir)
	if err != nil {
		return zero, limits, fmt.Errorf("DATA_DIR %q is not usable: %w", dataDir, err)
	}
	if !info.IsDir() {
		return zero, limits, fmt.Errorf("DATA_DIR %q is not a directory", dataDir)
	}
	readyzToken, err := readSecret("READYZ_TOKEN", 16, 128)
	if err != nil {
		return zero, limits, err
	}
	// 用户表固定读 DATA_DIR/users.yaml：容器部署时把 Secret 挂成这个文件，
	// 本地部署把 users.yaml 放进数据目录。
	users, err := app.LoadUsersFile(filepath.Join(dataDir, usersFileName))
	if err != nil {
		return zero, limits, err
	}
	sessionKey, err := readSecret("SESSION_SIGNING_KEY", 32, 128)
	if err != nil {
		return zero, limits, err
	}
	if sessionKey == "" {
		return zero, limits, errors.New("SESSION_SIGNING_KEY must be set")
	}
	urlKey, err := readSecret("URL_SIGNATURE_KEY", 32, 128)
	if err != nil {
		return zero, limits, err
	}
	if urlKey == "" {
		return zero, limits, errors.New("URL_SIGNATURE_KEY must be set")
	}
	return config{
		app: app.Config{
			DataDir:           dataDir,
			TrustedProxies:    os.Getenv("TRUSTED_PROXIES"),
			SessionSigningKey: sessionKey,
			URLSignatureKey:   urlKey,
			ReadyzToken:       readyzToken,
			Users:             users,
			FilesDir:          filesDir,
		},
		host: host,
		port: port,
	}, limits, nil
}

// checkTimezone 读 TZ：设了就必须是 Go 认得的地名。名字写错时 Go 会静默退回 UTC，
// 日志时间戳跟着错 —— 宁可在启动时报出来。
func checkTimezone() error {
	name := os.Getenv("TZ")
	if name == "" {
		return nil
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("TZ is not a valid time zone name %q: %w", name, err)
	}
	return nil
}

// readHost 读 HOST：没设就用 0.0.0.0，设了就必须是 IP 或合法的 RFC 1123 主机名 ——
// 带空格、斜杠的值会让 net.JoinHostPort 拼成另一个地址，宁可在启动时拦下。
func readHost() (string, error) {
	host := os.Getenv("HOST")
	if host == "" {
		return "0.0.0.0", nil
	}
	if net.ParseIP(host) == nil && !validHostname(host) {
		return "", fmt.Errorf("HOST must be an IP address or a valid hostname, got %q", host)
	}
	return host, nil
}

// validHostname 按 RFC 1123 校验：点分标签各 1-63 字符 [A-Za-z0-9-]、不以连字符起止，总长 <= 253。
func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

// readPort 读 PORT：没设就用 8080，设了就必须是 1-65535 的整数。
func readPort() (int, error) {
	raw := os.Getenv("PORT")
	if raw == "" {
		return 8080, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 65535 {
		return 0, fmt.Errorf("PORT must be an integer between 1 and 65535, got %q", raw)
	}
	return value, nil
}

// readConcurrencyLimits 读三个槽位数，没设就用默认值。
func readConcurrencyLimits() (concurrencyLimits, error) {
	var limits concurrencyLimits
	var err error
	if limits.auth, err = readLimit("MAX_AUTH_CONCURRENCY", api.DefaultAuthConcurrency); err != nil {
		return concurrencyLimits{}, err
	}
	if limits.api, err = readLimit("MAX_API_CONCURRENCY", api.DefaultAPIConcurrency); err != nil {
		return concurrencyLimits{}, err
	}
	if limits.static, err = readLimit("MAX_STATIC_CONCURRENCY", api.DefaultStaticConcurrency); err != nil {
		return concurrencyLimits{}, err
	}
	return limits, nil
}

// readLimit 读一个正整数上限：没设就用默认值，设了就必须 >= 1 —— 信号量容量为 0
// 会让服务永久排队，宁可在启动时就报错。
func readLimit(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be an integer >= 1, got %q", name, raw)
	}
	return value, nil
}

// readDataDir 读 DATA_DIR：未设置或只写空白时用默认 /data。
// 目录必须已经存在（卷挂载或部署方手工建），由 app.Open 校验并收紧权限 —— 缺了就当场失败，
// 不顺手建一个目录把写错的部署掩盖过去。
func readDataDir() string {
	if value := strings.TrimSpace(os.Getenv("DATA_DIR")); value != "" {
		return value
	}
	return defaultDataDir
}

// readFilesDir 读 FILES_DIR：未设置或只写空白时用默认 /data/files。
// 目录必须已经存在（只读卷挂载或部署方手工建），可读性由 app.Open 校验。
func readFilesDir() string {
	if value := strings.TrimSpace(os.Getenv("FILES_DIR")); value != "" {
		return value
	}
	return defaultFilesDir
}

// readSecret 读一个凭据类环境变量：长度与字符集都对得上才收，读完立刻从进程环境表里删掉 ——
// 免得子进程把它继承过去。留空返回空串，是否必填由调用方决定。
func readSecret(name string, minLength, maxLength int) (string, error) {
	value := os.Getenv(name)
	_ = os.Unsetenv(name)
	if value == "" {
		return "", nil
	}
	if len(value) < minLength || len(value) > maxLength {
		return "", fmt.Errorf("%s must be %d-%d characters long, got %d", name, minLength, maxLength, len(value))
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return "", fmt.Errorf("%s may only contain letters, digits and underscores, got %q", name, r)
		}
	}
	return value, nil
}
