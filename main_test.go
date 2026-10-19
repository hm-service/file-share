package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"file-share/api"
	"file-share/core"
	"file-share/internal/testenv"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// testSite 是一份最小的构建产物：两个页面、一个带内容哈希的资源、一个固定名字的图标，
// 外加一个目录（目录列表必须给不出来）。
func testSite() fs.FS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>文件分享</title>")},
		"auth.html":             {Data: []byte("<!doctype html><title>登录</title>")},
		"favicon.svg":           {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		"assets/app-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/app-abc123.css": {Data: []byte("body{}")},
		"assets/nested":         {Mode: fs.ModeDir},
	}
}

// 页面按 URL 表分发：/ → index.html，/auth → auth.html，两个页面都 no-store。
func TestSitePages(t *testing.T) {
	handler := siteHandler(testSite())

	cases := []struct {
		path string
		file string
	}{
		{"/", "文件分享"},
		{"/auth", "登录"},
	}
	for _, item := range cases {
		t.Run(item.path, func(t *testing.T) {
			recorder := testenv.Do(handler, http.MethodGet, item.path, "")
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s 状态码 = %d，想要 200", item.path, recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Errorf("%s Content-Type = %q", item.path, got)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s Cache-Control = %q，想要 no-store", item.path, got)
			}
			if !strings.Contains(recorder.Body.String(), item.file) {
				t.Errorf("%s 响应体 = %q，不含 %q", item.path, recorder.Body.String(), item.file)
			}
		})
	}
}

// 产物里的其余文件按原样发：带哈希的 /assets/* 一年 immutable，固定名字的每次回源确认。
func TestSiteServesBuiltFiles(t *testing.T) {
	handler := siteHandler(testSite())

	script := testenv.Do(handler, http.MethodGet, "/assets/app-abc123.js", "")
	if script.Code != http.StatusOK {
		t.Fatalf("/assets/app-abc123.js 状态码 = %d", script.Code)
	}
	if got := script.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Errorf("Content-Type = %q", got)
	}
	if got := script.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("/assets/* Cache-Control = %q", got)
	}

	style := testenv.Do(handler, http.MethodGet, "/assets/app-abc123.css", "")
	if got := style.Header().Get("Content-Type"); !strings.Contains(got, "text/css") {
		t.Errorf("CSS Content-Type = %q", got)
	}

	icon := testenv.Do(handler, http.MethodGet, "/favicon.svg", "")
	if icon.Code != http.StatusOK {
		t.Fatalf("/favicon.svg 状态码 = %d", icon.Code)
	}
	if got := icon.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/favicon.svg Cache-Control = %q，想要 no-cache", got)
	}

	// HEAD 走 GET 的处理器，但不回体。
	head := testenv.Do(handler, http.MethodHead, "/assets/app-abc123.js", "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD /assets/app-abc123.js = %d，体长 %d", head.Code, head.Body.Len())
	}
}

// 只认具体文件：目录不给列表，产物里的 *.html 不能绕过页面表直接访问，穿越也读不出东西。
func TestSiteHasNoDirectoryListing(t *testing.T) {
	handler := siteHandler(testSite())

	for _, path := range []string{"/assets/", "/assets/nested", "/assets/nested/", "/assets/nope.js",
		"/index.html", "/auth.html", "/nope", "/nope/deep.js"} {
		t.Run(path, func(t *testing.T) {
			recorder := testenv.Do(handler, http.MethodGet, path, "")
			if recorder.Code != http.StatusNotFound {
				t.Errorf("%s 状态码 = %d，想要 404", path, recorder.Code)
			}
			if strings.Contains(recorder.Body.String(), "app-abc123") {
				t.Errorf("%s 回了个目录列表: %s", path, recorder.Body.String())
			}
		})
	}

	// 路径穿越：ServeMux 先按清理后的路径回 301，跟过去也只会落到普通路径；这里只要求
	// 「不是带着内容 200」，不会从内嵌文件系统里读出别的东西。
	recorder := testenv.Do(handler, http.MethodGet, "/assets/../../etc/passwd", "")
	if recorder.Code == http.StatusOK && recorder.Body.Len() > 0 {
		t.Fatalf("路径穿越回了个 200：%s", recorder.Body.String())
	}
}

func TestSiteMethodNotAllowed(t *testing.T) {
	handler := siteHandler(testSite())

	for _, path := range []string{"/", "/auth", "/assets/app-abc123.js", "/favicon.svg"} {
		t.Run(path, func(t *testing.T) {
			recorder := testenv.Do(handler, http.MethodPost, path, "")
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s 状态码 = %d，想要 405", path, recorder.Code)
			}
			if got := recorder.Header().Get("Allow"); got != allowMethods {
				t.Errorf("POST %s Allow = %q，想要 %q", path, got, allowMethods)
			}
		})
	}
}

// 产物里少了页面说明这个二进制本身是坏的：500，不假装 404。
func TestSiteMissingPageIsServerError(t *testing.T) {
	handler := siteHandler(fstest.MapFS{"favicon.svg": {Data: []byte("<svg/>")}})
	recorder := testenv.Do(handler, http.MethodGet, "/", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("页面缺失时状态码 = %d，想要 500", recorder.Code)
	}
}

func defaultLimits() concurrencyLimits {
	return concurrencyLimits{
		auth:   api.DefaultAuthConcurrency,
		api:    api.DefaultAPIConcurrency,
		static: api.DefaultStaticConcurrency,
	}
}

// 静态那半给 0 个槽位时页面一直排队，其余路由组各有各的槽位、照常应答。
func TestRootHandlerGivesEachRouteGroupItsOwnGate(t *testing.T) {
	instance := testenv.New(t)
	handler := newRootHandler(instance, testSite(), concurrencyLimits{auth: 1, api: 1, static: 0})

	// 页面与 /s/* 共用静态闸门：0 个槽位时两者都该排队。
	for _, target := range []string{"/", "/s/whatever"} {
		blocked := make(chan struct{})
		go func() {
			defer close(blocked)
			testenv.Do(handler, http.MethodGet, target, "")
		}()
		select {
		case <-blocked:
			t.Fatalf("静态那半是 0 个槽位，却把 %s 放过去了", target)
		case <-time.After(200 * time.Millisecond):
		}
	}

	for name, item := range map[string]struct {
		target string
		want   int
	}{
		"存活探针不设闸门":   {"/livez", http.StatusOK},
		"就绪探针有自己的槽位": {"/readyz", http.StatusOK},
		"接口那半有自己的槽位": {"/api/files", http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			if recorder := testenv.Do(handler, http.MethodGet, item.target, ""); recorder.Code != item.want {
				t.Fatalf("%s = %d，想要 %d", item.target, recorder.Code, item.want)
			}
		})
	}
}

// 认证与业务各一组：认证那半给 0 个槽位时它一直排队，业务那半照常应答。
func TestRootHandlerKeepsAuthGateSeparate(t *testing.T) {
	instance := testenv.New(t)
	handler := newRootHandler(instance, testSite(), concurrencyLimits{auth: 0, api: 1, static: 1})

	authDone := make(chan struct{})
	go func() {
		defer close(authDone)
		testenv.DoJSON(handler, http.MethodPost, "/api/auth", map[string]any{"token": "wrong"}, "")
	}()
	select {
	case <-authDone:
		t.Fatal("认证那半是 0 个槽位，却把登录放过去了")
	case <-time.After(200 * time.Millisecond):
	}

	if recorder := testenv.Do(handler, http.MethodGet, "/api/files", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("/api/files = %d，想要 401", recorder.Code)
	}
	if recorder := testenv.Do(handler, http.MethodGet, "/", ""); recorder.Code != http.StatusOK {
		t.Fatalf("/ = %d，想要 200", recorder.Code)
	}
}

func TestReadPort(t *testing.T) {
	cases := []struct {
		name    string
		port    string
		want    int
		wantErr bool
	}{
		{name: "没设用默认值", want: 8080},
		{name: "正常端口", port: "10080", want: 10080},
		{name: "下界", port: "1", want: 1},
		{name: "上界", port: "65535", want: 65535},
		{name: "0", port: "0", wantErr: true},
		{name: "超出上界", port: "65536", wantErr: true},
		{name: "负数", port: "-1", wantErr: true},
		{name: "不是数字", port: "http", wantErr: true},
		{name: "带空格", port: " 8080", wantErr: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("PORT", item.port)
			got, err := readPort()
			if item.wantErr {
				if err == nil {
					t.Fatalf("readPort() = %d，想要报错", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readPort() 报错: %v", err)
			}
			if got != item.want {
				t.Errorf("readPort() = %d，想要 %d", got, item.want)
			}
		})
	}
}

func TestReadHost(t *testing.T) {
	longest := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	cases := []struct {
		name    string
		host    string
		want    string
		wantErr bool
	}{
		{name: "没设用默认值", want: "127.0.0.1"},
		{name: "IPv4", host: "0.0.0.0", want: "0.0.0.0"},
		{name: "IPv6", host: "::1", want: "::1"},
		{name: "主机名", host: "api.example.com", want: "api.example.com"},
		{name: "单标签主机名", host: "localhost", want: "localhost"},
		{name: "253 字符名", host: longest, want: longest},
		{name: "254 字符名", host: longest + "a", wantErr: true},
		{name: "带空格", host: " 127.0.0.1", wantErr: true},
		{name: "带端口", host: "localhost:8080", wantErr: true},
		{name: "空标签", host: "a..b", wantErr: true},
		{name: "结尾点", host: "example.com.", wantErr: true},
		{name: "连字符开头", host: "-host", wantErr: true},
		{name: "下划线", host: "my_host", wantErr: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("HOST", item.host)
			got, err := readHost()
			if item.wantErr {
				if err == nil {
					t.Fatalf("readHost() = %q，想要报错", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readHost() 报错: %v", err)
			}
			if got != item.want {
				t.Errorf("readHost() = %q，想要 %q", got, item.want)
			}
		})
	}
}

func TestReadConcurrencyLimits(t *testing.T) {
	cases := []struct {
		name    string
		auth    string
		api     string
		static  string
		want    concurrencyLimits
		wantErr bool
	}{
		{name: "都没设用默认值", want: concurrencyLimits{auth: 32, api: 128, static: 64}},
		{name: "都设了", auth: "4", api: "256", static: "8", want: concurrencyLimits{auth: 4, api: 256, static: 8}},
		{name: "只设了一个", api: "1", want: concurrencyLimits{auth: 32, api: 1, static: 64}},
		{name: "认证不是数字", auth: "many", wantErr: true},
		{name: "认证是 0", auth: "0", wantErr: true},
		{name: "API 是负数", api: "-3", wantErr: true},
		{name: "静态是 0", static: "0", wantErr: true},
		{name: "静态带空格", static: " 8", wantErr: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("MAX_AUTH_CONCURRENCY", item.auth)
			t.Setenv("MAX_API_CONCURRENCY", item.api)
			t.Setenv("MAX_STATIC_CONCURRENCY", item.static)
			got, err := readConcurrencyLimits()
			if item.wantErr {
				if err == nil {
					t.Fatalf("readConcurrencyLimits() = %+v，想要报错", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readConcurrencyLimits() 报错: %v", err)
			}
			if got != item.want {
				t.Errorf("readConcurrencyLimits() = %+v，想要 %+v", got, item.want)
			}
		})
	}
}

func TestReadMaxUploadBytes(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "没设用默认值", want: 8589934592},
		{name: "正常值", raw: "1024", want: 1024},
		{name: "下界", raw: "1", want: 1},
		{name: "0", raw: "0", wantErr: true},
		{name: "负数", raw: "-1", wantErr: true},
		{name: "不是数字", raw: "8GiB", wantErr: true},
		{name: "带空格", raw: " 1024", wantErr: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("MAX_UPLOAD_BYTES", item.raw)
			got, err := readMaxUploadBytes()
			if item.wantErr {
				if err == nil {
					t.Fatalf("readMaxUploadBytes() = %d，想要报错", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readMaxUploadBytes() 报错: %v", err)
			}
			if got != item.want {
				t.Errorf("readMaxUploadBytes() = %d，想要 %d", got, item.want)
			}
		})
	}
}

func TestReadDataDir(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	if got := readDataDir(); got != defaultDataDir {
		t.Errorf("没设 DATA_DIR 时 = %q，想要默认 %q", got, defaultDataDir)
	}
	t.Setenv("DATA_DIR", "   ")
	if got := readDataDir(); got != defaultDataDir {
		t.Errorf("只写空白时 = %q，想要默认 %q", got, defaultDataDir)
	}
	t.Setenv("DATA_DIR", " /tmp/data ")
	if got := readDataDir(); got != "/tmp/data" {
		t.Errorf("readDataDir() = %q", got)
	}
}

func TestCheckTimezone(t *testing.T) {
	cases := []struct {
		name    string
		tz      string
		wantErr bool
	}{
		{name: "没设"},
		{name: "UTC", tz: "UTC"},
		{name: "Asia/Shanghai", tz: "Asia/Shanghai"},
		{name: "地名写错", tz: "Asia/Shanghi", wantErr: true},
		{name: "不是地名", tz: "Shanghai", wantErr: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("TZ", item.tz)
			if err := checkTimezone(); (err != nil) != item.wantErr {
				t.Fatalf("checkTimezone() 报错 = %v，想要报错 = %v", err, item.wantErr)
			}
		})
	}
}

// 凭据读完要从环境表里删掉，且长度 / 字符集都要卡住。
func TestReadSecret(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "留空不校验"},
		{name: "正常值", value: strings.Repeat("a", 16)},
		{name: "太短", value: "short", wantErr: true},
		{name: "太长", value: strings.Repeat("a", 129), wantErr: true},
		{name: "含连字符", value: "abc-def-ghi-jkl-mno", wantErr: true},
		{name: "含空格", value: "abc def ghi jkl mno", wantErr: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("TEST_SECRET", item.value)
			got, err := readSecret("TEST_SECRET", 16, 128)
			if item.wantErr {
				if err == nil {
					t.Fatalf("readSecret() = %q，想要报错", got)
				}
			} else {
				if err != nil {
					t.Fatalf("readSecret() 报错: %v", err)
				}
				if got != item.value {
					t.Errorf("readSecret() = %q，想要 %q", got, item.value)
				}
			}
			if left := os.Getenv("TEST_SECRET"); left != "" {
				t.Errorf("读完之后不该还留在环境表里: %q", left)
			}
		})
	}
}

// loadConfig 把必填项凑齐后能组装出配置，缺任何一项都报错。
func TestLoadConfig(t *testing.T) {
	t.Run("完整配置", func(t *testing.T) {
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("AUTH_TOKEN", testenv.AuthToken)
		t.Setenv("SESSION_SIGNING_KEY", testenv.SessionSigningKey)
		t.Setenv("READYZ_TOKEN", testenv.ReadyzToken)
		t.Setenv("HOST", "0.0.0.0")
		t.Setenv("PORT", "18080")
		t.Setenv("TZ", "")
		t.Setenv("MAX_UPLOAD_BYTES", "4096")

		cfg, limits, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() 报错: %v", err)
		}
		if cfg.host != "0.0.0.0" || cfg.port != 18080 {
			t.Errorf("监听地址 = %s:%d", cfg.host, cfg.port)
		}
		if limits.api != api.DefaultAPIConcurrency {
			t.Errorf("limits = %+v", limits)
		}
		if cfg.app.MaxUploadBytes != 4096 || cfg.app.ReadyzToken != testenv.ReadyzToken {
			t.Errorf("app 配置 = %+v", cfg.app)
		}
		ok, err := core.VerifyToken(testenv.AuthToken, cfg.app.AuthTokenHash)
		if err != nil || !ok {
			t.Errorf("AUTH_TOKEN 摘要不可用: ok=%v err=%v", ok, err)
		}
		if left := os.Getenv("AUTH_TOKEN"); left != "" {
			t.Errorf("AUTH_TOKEN 应当已从环境表删除: %q", left)
		}
		if left := os.Getenv("SESSION_SIGNING_KEY"); left != "" {
			t.Errorf("SESSION_SIGNING_KEY 应当已从环境表删除: %q", left)
		}
	})

	t.Run("DATA_DIR 未设置时用默认 /data", func(t *testing.T) {
		t.Setenv("DATA_DIR", "")
		t.Setenv("AUTH_TOKEN", testenv.AuthToken)
		t.Setenv("SESSION_SIGNING_KEY", testenv.SessionSigningKey)
		t.Setenv("READYZ_TOKEN", "")
		cfg, _, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() 报错: %v", err)
		}
		if cfg.app.DataDir != defaultDataDir {
			t.Errorf("DataDir = %q，想要默认 %q", cfg.app.DataDir, defaultDataDir)
		}
	})

	missingCases := []struct {
		name   string
		mutate func()
	}{
		{name: "缺 AUTH_TOKEN", mutate: func() { t.Setenv("AUTH_TOKEN", "") }},
		{name: "缺 SESSION_SIGNING_KEY", mutate: func() { t.Setenv("SESSION_SIGNING_KEY", "") }},
		{name: "AUTH_TOKEN 太短", mutate: func() { t.Setenv("AUTH_TOKEN", "short") }},
	}
	for _, item := range missingCases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			t.Setenv("AUTH_TOKEN", testenv.AuthToken)
			t.Setenv("SESSION_SIGNING_KEY", testenv.SessionSigningKey)
			t.Setenv("READYZ_TOKEN", "")
			t.Setenv("HOST", "")
			t.Setenv("PORT", "")
			t.Setenv("TZ", "")
			item.mutate()
			if _, _, err := loadConfig(); err == nil {
				t.Fatal("缺配置时 loadConfig() 应当报错")
			}
		})
	}
}

// 每条日志都要带 RFC 3339 的 timestamp 字段，而不是 handler 默认的 time。
func TestInitLoggerTimestampField(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	previousOut := os.Stdout
	previousLogger := slog.Default()
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previousOut
		slog.SetDefault(previousLogger)
	})

	initLogger()
	slog.Info("probe")
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭管道失败: %v", err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &record); err != nil {
		t.Fatalf("不是合法 JSON: %v (%s)", raw, err)
	}
	stamp, ok := record["timestamp"].(string)
	if !ok {
		t.Fatalf("缺少 timestamp 字段: %v", record)
	}
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		t.Fatalf("timestamp 不是 RFC 3339: %q", stamp)
	}
	if _, ok := record["time"]; ok {
		t.Fatalf("不应再有 time 字段: %v", record)
	}
	if record["msg"] != "probe" {
		t.Errorf("msg = %v", record["msg"])
	}
}
