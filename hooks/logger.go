package hooks

import (
	"time"

	"github.com/tiyee/holy/components/log"
	"github.com/tiyee/holy/engine"
)

// Logger 创建记录请求日志的应用守卫。
func Logger(logger *log.Logger) engine.HandlerFunc {
	return func(c *engine.Context) {
		started := time.Now()
		c.Next()
		logger.Log(c.Ctx(), log.LevelInfo, "http request",
			log.String("method", c.Request().Method),
			log.String("path", c.Request().URL.Path),
			log.Int("status", c.Status()),
			log.Duration("duration", time.Since(started)),
		)
	}
}
