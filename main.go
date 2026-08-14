package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed static
var staticFS embed.FS

var staticSub = mustSub(staticFS, "static")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

type app struct {
	db       *sql.DB
	dataDir  string
	filesDir string
	favPath  string
	fileLock sync.Mutex
	favMu    sync.Mutex
}

func newApp(dataDir string) (*app, error) {
	a := &app{
		dataDir:  dataDir,
		filesDir: filepath.Join(dataDir, "files"),
		favPath:  filepath.Join(dataDir, "favorite.json"),
	}
	for _, dir := range []string{a.dataDir, a.filesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	var err error
	a.db, err = sql.Open("sqlite", filepath.Join(dataDir, "files.db"))
	if err != nil {
		return nil, err
	}
	// 单连接串行访问，避免 SQLite 并发写入冲突
	a.db.SetMaxOpenConns(1)

	if _, err := a.db.Exec(`CREATE TABLE IF NOT EXISTS files (
		id TEXT PRIMARY KEY,
		filename TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		update_time INTEGER NOT NULL
	)`); err != nil {
		return nil, err
	}

	if _, err := os.Stat(a.favPath); os.IsNotExist(err) {
		if err := os.WriteFile(a.favPath, []byte("[]"), 0o644); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.indexHandler)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("POST /api/check_file", a.checkFileHandler)
	mux.HandleFunc("POST /api/upload", a.uploadHandler)
	mux.HandleFunc("GET /api/files", a.listFilesHandler)
	mux.HandleFunc("POST /api/favorite/{file_id}", a.toggleFavoriteHandler)
	mux.HandleFunc("GET /api/download/{file_id}", a.downloadHandler)
	mux.HandleFunc("DELETE /api/files/{file_id}", a.deleteHandler)
	return mux
}

func (a *app) indexHandler(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html 不存在", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// 文件秒传预检
func (a *app) checkFileHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "无效的 multipart 请求", http.StatusBadRequest)
		return
	}
	clientSHA := strings.TrimSpace(r.FormValue("client_sha256"))
	filename := r.FormValue("filename")
	if !isValidSHA256(clientSHA) || filename == "" {
		http.Error(w, "缺少或无效的 client_sha256 / filename", http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(filepath.Join(a.filesDir, clientSHA)); err == nil {
		fileID := newID()
		if _, err := a.db.Exec(
			"INSERT INTO files (id, filename, sha256, update_time) VALUES (?, ?, ?, ?)",
			fileID, filename, clientSHA, time.Now().Unix(),
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"exists": true, "id": fileID, "message": "秒传成功"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": false})
}

// 上传：流式写入临时文件 -> SHA256 校验 -> 重命名为正式文件 -> 写库
func (a *app) uploadHandler(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "无效的 multipart 请求", http.StatusBadRequest)
		return
	}

	var clientSHA, filename string
	var serverSHA string
	hasFile := false
	// 字段顺序不固定，file 可能先于 client_sha256 到达，
	// 因此遇到 file 时必须立即流式落盘，否则 NextPart() 会丢弃其内容
	tempPath := filepath.Join(a.filesDir, ".upload-"+newID()+".tmp")

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "解析 multipart 失败", http.StatusBadRequest)
			return
		}
		switch part.FormName() {
		case "client_sha256":
			b, _ := io.ReadAll(part)
			clientSHA = strings.TrimSpace(string(b))
		case "file":
			filename = part.FileName()
			f, err := os.Create(tempPath)
			if err != nil {
				http.Error(w, "创建临时文件失败", http.StatusInternalServerError)
				return
			}
			hasFile = true
			hasher := sha256.New()
			if _, err := io.Copy(io.MultiWriter(f, hasher), part); err != nil {
				f.Close()
				os.Remove(tempPath)
				http.Error(w, "读取上传数据失败", http.StatusBadRequest)
				return
			}
			if err := f.Close(); err != nil {
				os.Remove(tempPath)
				http.Error(w, "关闭临时文件失败", http.StatusInternalServerError)
				return
			}
			serverSHA = hex.EncodeToString(hasher.Sum(nil))
		}
	}
	if !isValidSHA256(clientSHA) || !hasFile || filename == "" {
		os.Remove(tempPath)
		http.Error(w, "缺少 file 或 client_sha256", http.StatusBadRequest)
		return
	}

	a.fileLock.Lock()
	defer a.fileLock.Unlock()

	fileID := newID()
	finalPath := filepath.Join(a.filesDir, clientSHA)

	if serverSHA != clientSHA {
		os.Remove(tempPath)
		http.Error(w, "文件哈希校验失败，上传数据可能已损坏或被篡改", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(finalPath); os.IsNotExist(err) {
		if err := os.Rename(tempPath, finalPath); err != nil {
			os.Remove(tempPath)
			http.Error(w, "文件落盘失败", http.StatusInternalServerError)
			return
		}
	} else {
		// 物理文件已存在（秒传并发场景），丢弃本次上传内容，仅补一条记录
		os.Remove(tempPath)
	}

	if _, err := a.db.Exec(
		"INSERT INTO files (id, filename, sha256, update_time) VALUES (?, ?, ?, ?)",
		fileID, filename, clientSHA, time.Now().Unix(),
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "上传成功", "id": fileID, "sha256": clientSHA})
}

func (a *app) listFilesHandler(w http.ResponseWriter, r *http.Request) {
	favSet := map[string]bool{}
	if ids, err := a.loadFavorites(); err == nil {
		for _, id := range ids {
			favSet[id] = true
		}
	}

	rows, err := a.db.Query("SELECT id, filename, sha256, update_time FROM files ORDER BY update_time DESC")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	defer rows.Close()

	type fileItem struct {
		ID         string `json:"id"`
		Filename   string `json:"filename"`
		SHA256     string `json:"sha256"`
		UpdateTime int64  `json:"update_time"`
		Size       int64  `json:"size"`
		IsFavorite bool   `json:"is_favorite"`
	}
	files := make([]fileItem, 0)
	for rows.Next() {
		var f fileItem
		if err := rows.Scan(&f.ID, &f.Filename, &f.SHA256, &f.UpdateTime); err != nil {
			continue
		}
		if st, err := os.Stat(filepath.Join(a.filesDir, f.SHA256)); err == nil {
			f.Size = st.Size()
		}
		f.IsFavorite = favSet[f.ID]
		files = append(files, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (a *app) toggleFavoriteHandler(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("file_id")

	var one int
	if err := a.db.QueryRow("SELECT 1 FROM files WHERE id = ?", fileID).Scan(&one); err != nil {
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}

	a.favMu.Lock()
	defer a.favMu.Unlock()

	ids, err := a.loadFavorites()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	status := "favorited"
	found := false
	for i, id := range ids {
		if id == fileID {
			ids = append(ids[:i], ids[i+1:]...)
			status = "unfavorited"
			found = true
			break
		}
	}
	if !found {
		ids = append(ids, fileID)
	}
	if err := a.saveFavorites(ids); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (a *app) downloadHandler(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("file_id")

	var filename, sha string
	if err := a.db.QueryRow("SELECT filename, sha256 FROM files WHERE id = ?", fileID).Scan(&filename, &sha); err != nil {
		http.Error(w, "文件记录不存在", http.StatusNotFound)
		return
	}
	path := filepath.Join(a.filesDir, sha)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "物理文件已丢失", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=utf-8''%s", url.QueryEscape(filename)))
	http.ServeFile(w, r, path)
}

func (a *app) deleteHandler(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("file_id")

	a.fileLock.Lock()
	defer a.fileLock.Unlock()

	var sha string
	if err := a.db.QueryRow("SELECT sha256 FROM files WHERE id = ?", fileID).Scan(&sha); err != nil {
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}
	if _, err := a.db.Exec("DELETE FROM files WHERE id = ?", fileID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}

	var refCount int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM files WHERE sha256 = ?", sha).Scan(&refCount); err == nil && refCount == 0 {
		os.Remove(filepath.Join(a.filesDir, sha))
	}

	a.favMu.Lock()
	if ids, err := a.loadFavorites(); err == nil {
		for i, id := range ids {
			if id == fileID {
				ids = append(ids[:i], ids[i+1:]...)
				break
			}
		}
		_ = a.saveFavorites(ids)
	}
	a.favMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"message": "删除成功"})
}

func (a *app) loadFavorites() ([]string, error) {
	data, err := os.ReadFile(a.favPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

func (a *app) saveFavorites(ids []string) error {
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.favPath, data, 0o644)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func isValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand 失败属于系统级错误
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dataDir := envOr("DATA_DIR", "/data")
	a, err := newApp(dataDir)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	port := envOr("PORT", "8080")
	addr := ":" + port
	log.Printf("文件服务启动: http://0.0.0.0%s (DATA_DIR=%s)", addr, dataDir)
	if err := http.ListenAndServe(addr, corsMiddleware(a.routes())); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
