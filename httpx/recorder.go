package httpx

import "net/http"

// StatusRecorder 记下响应状态码，供访问日志使用。
//
// 实现 Unwrap 是为了让 http.ResponseController / ServeContent 这类会往下要
// Flusher、ReaderFrom 的调用方仍能拿到原始 ResponseWriter。
type StatusRecorder struct {
	http.ResponseWriter
	status int
}

func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (rec *StatusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *StatusRecorder) Write(body []byte) (int, error) {
	return rec.ResponseWriter.Write(body)
}

// Status 返回最终状态码；处理函数什么都没写时是 200。
func (rec *StatusRecorder) Status() int { return rec.status }

func (rec *StatusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }
