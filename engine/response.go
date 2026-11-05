package engine

import (
	"bufio"
	"html"
	"io"
	"net"
	"net/http"
)

// redirectExactPath 输出已转义的站内目标，不清洗路由中的重复斜杠或点段。
// 保留普通重定向的 Content-Type 与 GET 提示正文，HEAD 不输出正文。
func redirectExactPath(w http.ResponseWriter, r *http.Request, target string, code int) {
	h := w.Header()
	_, hadContentType := h["Content-Type"]
	h.Set("Location", target)
	if !hadContentType && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(code)
	if !hadContentType && r.Method == http.MethodGet {
		_, _ = io.WriteString(w, "<a href=\""+html.EscapeString(target)+"\">"+http.StatusText(code)+"</a>.\n\n")
	}
}

// responseWriter 记录响应提交状态，Unwrap 允许 ResponseController 访问底层连接。
type responseWriter struct {
	http.ResponseWriter
	status, size int
}

func (w *responseWriter) reset(writer http.ResponseWriter) {
	w.ResponseWriter, w.status, w.size = writer, http.StatusOK, -1
}
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) Written() bool               { return w.size >= 0 }
func (w *responseWriter) WriteHeader(code int) {
	if w.Written() {
		return
	}
	w.ResponseWriter.WriteHeader(code)
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		return
	}
	w.status, w.size = code, 0
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if !w.Written() {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.size += n
	return n, err
}
func (w *responseWriter) FlushError() error {
	if !w.Written() {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *responseWriter) Flush() { _ = w.FlushError() }
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.size = 0
	}
	return conn, rw, err
}
func (w *responseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}
