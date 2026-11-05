package handles

import (
	"errors"
	"github.com/tiyee/holy/engine"
	"net/http"
)

// User 演示按方法注册 REST 处理器。
type User struct{}

func (User) GET(c *engine.Context) { c.JSON(200, map[string]string{"id": c.Param("id")}) }
func (User) POST(c *engine.Context) {
	var input struct {
		Name string `json:"name"`
	}
	if err := c.BindJSON(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.Error("request body too large", http.StatusRequestEntityTooLarge)
		} else {
			c.Error("invalid JSON", http.StatusBadRequest)
		}
		return
	}
	c.JSON(201, input)
}
