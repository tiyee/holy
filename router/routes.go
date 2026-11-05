package router

import (
	"github.com/tiyee/holy/engine"
	"github.com/tiyee/holy/handles"
	"github.com/tiyee/holy/hooks"
)

func LoadRouter(r *engine.Engine) {
	r.Middleware(hooks.Logger)
	r.GET("/health", func(c *engine.Context) { c.JSON(200, map[string]string{"status": "ok"}) })
	api := r.Group("/api/v1")
	api.Rest("/users/:id", handles.User{})
	api.OPTIONS("/users/:id", func(c *engine.Context) {
		c.Response().Header().Set("Allow", "GET, POST, OPTIONS")
		c.AbortWithStatus(204)
	})
	r.GET("/files/*path", func(c *engine.Context) { c.JSON(200, map[string]string{"path": c.Param("path")}) })
}
