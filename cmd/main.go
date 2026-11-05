package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tiyee/holy/components/log"
	"github.com/tiyee/holy/engine"
	"github.com/tiyee/holy/router"
)

func main() {
	if err := run(); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func run() (result error) {
	if err := log.Init(log.Config{Format: "console", Level: "info"}); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, log.Close()) }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	e := engine.New(func(e *engine.Engine) {
		e.HandleMethodNotAllowed = true
		if addr := os.Getenv("HOLY_ADDR"); addr != "" {
			e.SetAddr(addr)
		}
	})
	router.LoadRouter(e)
	shutdown := make(chan error, 1)
	err := e.Run(func(server *http.Server) {
		go func() {
			<-ctx.Done()
			deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := server.Shutdown(deadline)
			if err != nil {
				err = errors.Join(err, server.Close())
			}
			shutdown <- err
		}()
	})
	stop()
	shutdownErr := <-shutdown
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, shutdownErr)
}
