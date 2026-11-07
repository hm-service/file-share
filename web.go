package main

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// built 是前端的构建产物（frontend/dist，由 just frontend 生成）。
// all: 前缀把点号开头的文件也一并嵌进来（Vite 会往 dist/.vite/ 写清单）。
//
//go:embed all:frontend/dist
var built embed.FS

// Dist 是剥掉 frontend/dist 前缀后的那一层：页面与资源都按 dist 内的相对路径找。
// 没跑过前端构建时这里在编译期就不成立（go:embed 报 no matching files），因此运行时不必兜底。
var Dist = mustSub(built, "frontend/dist")

func mustSub(files fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(files, dir)
	if err != nil {
		panic("嵌入的前端产物不可用: " + err.Error())
	}
	return sub
}

// allowMethods 是站点外壳认的方法：Go 的 ServeMux 把 HEAD 也交给 GET 的处理器。
const allowMethods = "GET, HEAD"

// pages 是对外页面地址到产物文件的映射。只有表里的路径能出 HTML：
// 产物里的 *.html 不能被直接访问（/index.html 之类一律 404），一个页面因此只有一个 URL。
var pages = map[string]string{
	"/":     "index.html",
	"/auth": "auth.html",
}

// siteHandler 是站点外壳：页面 + 构建产物里的其余文件（/assets/*、favicon.svg） + 前端视图路由。
// 接口、探针与公开下载都在 api 包里，由 main.go 按前缀挂到别的分支上 —— 这里只管
// 「把一个构建产物当成网站发出去」这一件事。
func siteHandler(files fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", allowMethods)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		// 多余的反斜杠、//、/a/../b 由 ServeMux 先清理并 301 掉，这里只面对规范路径。
		if file, ok := pages[r.URL.Path]; ok {
			servePage(w, files, file)
			return
		}
		// 先认产物里的具体文件：favicon.svg 这种名字也可能刚好是分组名，文件优先。
		name := strings.TrimPrefix(r.URL.Path, "/")
		if serveSiteFile(w, r, files, name) {
			return
		}
		// 单段路径末尾多一个斜杠（/cso2/）重定向到规范地址；目录（/assets/）不走这里。
		if trimmed, ok := strings.CutSuffix(r.URL.Path, "/"); ok && isViewRoute(trimmed) {
			if _, err := fs.Stat(files, strings.TrimPrefix(trimmed, "/")); err != nil {
				http.Redirect(w, r, trimmed, http.StatusMovedPermanently)
				return
			}
		}
		// 剩下的单段路径交给前端路由（/<分组名>、/favorites、/ungrouped）。
		if isViewRoute(r.URL.Path) {
			servePage(w, files, "index.html")
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// isViewRoute 判断路径是不是前端视图路由：一个非空的路径段，且不指向产物里的页面文件。
// 分组名由用户输入、直接作为这一段出现，所以这里只能按形状判断，不能枚举。
// 多段路径（/assets/xxx、/a/b）与 *.html 仍然只认产物里的真实文件，未知的一律 404。
func isViewRoute(path string) bool {
	segment, found := strings.CutPrefix(path, "/")
	if !found || segment == "" || strings.Contains(segment, "/") {
		return false
	}
	return !strings.HasSuffix(segment, ".html")
}

// servePage 把页面写出去：页面一律 no-store，免得改完前端还看旧的。
func servePage(w http.ResponseWriter, files fs.FS, file string) {
	body, err := fs.ReadFile(files, file)
	if err != nil {
		// 构建产物是嵌进二进制的：读不到就说明这个二进制本身是坏的，没有可兜的。
		http.Error(w, "page missing", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// serveSiteFile 分发产物里的具体文件；名字不指向文件（目录、未知路径、*.html）时返回 false，
// 由调用方决定是当视图路由还是 404。
//
// 不用 http.FileServerFS 就是为了不给目录列表：只认具体文件，目录（含 /assets/ 本身）
// 一律不给，免得把内嵌文件的清单摊开给人看。
//
// 缓存分三档：页面走 servePage 的 no-store；带内容哈希的 /assets/* 一年 immutable；
// 其余（favicon.svg 这种固定名字）每次回源确认。
func serveSiteFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) bool {
	if name == "" || name == "." || strings.HasSuffix(name, ".html") || !fs.ValidPath(name) {
		return false
	}
	info, err := fs.Stat(files, name)
	if err != nil || info.IsDir() {
		return false
	}
	body, err := fs.ReadFile(files, name)
	if err != nil {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// ServeContent 按扩展名定 Content-Type，顺手处理 Range 与条件请求。
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(body))
	return true
}
