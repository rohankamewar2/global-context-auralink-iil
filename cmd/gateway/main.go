// gateway is the only listener intended as a Cloudflare Tunnel origin. It keeps
// all private session/admin endpoints in cmd/server on a different port.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rohankamewar2/global-context-auralink-iil/internal/contextapi"
	"github.com/rohankamewar2/global-context-auralink-iil/internal/database"
)

func main() {
	port := flag.Int("port", 8082, "loopback gateway port")
	flag.Parse()
	if err := run(*port); err != nil {
		slog.Error("gateway stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func run(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("gateway port must be between 1 and 65535")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	c, err := contextapi.LoadConfig()
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("gateway requires backend tokens; run go run ./cmd/apiconfig")
	}
	h, err := contextapi.GatewayHandler(*c, contextapi.PGStore{Pool: pool})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Handler: h,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16384,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return errors.New("could not listen on gateway port; check port availability")
	}
	slog.Info("Sarvam scoped gateway started", "address", server.Addr, "session_auth", "service_bearer_and_session_capability", "writes_enabled", true)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("gateway HTTP server failed")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
			return errors.New("gateway graceful shutdown timed out")
		}
	}
	return nil
}
