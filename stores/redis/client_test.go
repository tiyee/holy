package redis

import (
	"context"
	"errors"
	"net"
	"testing"

	driver "github.com/redis/go-redis/v9"
)

func TestFailedOpenClosesClient(t *testing.T) {
	failure := errors.New("dial failed")
	opts := &driver.Options{
		Addr:          "unused:6379",
		MaxRetries:    -1,
		DialerRetries: 1,
		Dialer:        func(context.Context, string, string) (net.Conn, error) { return nil, failure },
	}
	var created *driver.Client
	client, err := open(context.Background(), opts, func(opts *driver.Options) *driver.Client {
		created = driver.NewClient(opts)
		t.Cleanup(func() { created.Close() })
		return created
	})
	if client != nil || !errors.Is(err, failure) {
		t.Fatalf("Open: %v %v", client, err)
	}
	if err := created.Close(); !errors.Is(err, driver.ErrClosed) {
		t.Fatalf("failed client was not closed: %v", err)
	}
}
