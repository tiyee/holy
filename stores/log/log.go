// Package log 提供应用日志的创建入口。
package log

import componentlog "github.com/tiyee/holy/components/log"

// Open 创建应用持有的 Logger。调用方应在退出前关闭该实例。
func Open(cfg componentlog.Config) (*componentlog.Logger, error) {
	return componentlog.New(cfg)
}
