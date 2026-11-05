package log

import (
	"io"
	"sync"
)

// lockedWriter 用互斥锁保证单次 Write 的原子性：
// 直写 stdout/stderr 时防止并发日志行交错（rotate/async writer 自带串行化，不受影响）。
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
