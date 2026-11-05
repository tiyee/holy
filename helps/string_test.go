package helps

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

func TestNonceStrFormat(t *testing.T) {
	value := NonceStr()
	if len(value) != 32 || value != strings.ToLower(value) {
		t.Fatalf("nonce 格式错误：%q", value)
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("nonce 不是十六进制：%v", err)
	}
}

func TestNonceStrConcurrent(t *testing.T) {
	const workers, perWorker = 32, 2000
	values := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				values <- NonceStr()
			}
		})
	}
	go func() {
		wg.Wait()
		close(values)
	}()

	seen := make(map[string]struct{}, workers*perWorker)
	duplicates := 0
	for value := range values {
		if _, exists := seen[value]; exists {
			duplicates++
		}
		seen[value] = struct{}{}
	}
	if duplicates != 0 {
		t.Fatalf("%d 次并发调用出现 %d 次重复", workers*perWorker, duplicates)
	}
}
