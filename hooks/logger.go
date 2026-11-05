package hooks

import (
	"github.com/tiyee/holy/components/log"
	"github.com/tiyee/holy/engine"
	"time"
)

// Logger 演示围绕请求执行的中间件。
func Logger(c *engine.Context) {
	started := time.Now()
	c.Next()
	log.Ctx(c.Ctx()).Info("http request", log.String("method", c.Request().Method), log.String("path", c.Request().URL.Path), log.Int("status", c.Status()), log.Duration("duration", time.Since(started)))
}
