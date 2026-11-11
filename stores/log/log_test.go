package log_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tiyee/holy/components/log"
	logstore "github.com/tiyee/holy/stores/log"
)

func TestOpenReturnsConfiguredLogger(t *testing.T) {
	var output bytes.Buffer
	logger, err := logstore.Open(log.Config{Writer: &output, Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if invalid, err := logstore.Open(log.Config{Format: "invalid"}); err == nil || invalid != nil {
		t.Fatal("无效日志配置应返回错误和 nil 实例")
	}

	logger.Info("filtered")
	logger.Warn("visible", log.String("source", "component"))
	var entry struct {
		Level   string `json:"level"`
		Message string `json:"msg"`
		Source  string `json:"source"`
	}
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("日志应恰好包含一条 JSON 记录，实际输出 %q，错误 %v", output.String(), err)
	}
	if entry.Level != "WARN" || entry.Message != "visible" || entry.Source != "component" {
		t.Fatalf("日志未使用注入的配置，实际输出 %q", output.String())
	}
}
