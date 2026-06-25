package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/abs/mysql-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeStdio runs the server over stdio until ctx is cancelled or the client
// disconnects. This is the transport used directly by MCP clients and by stdio
// MCP proxies, which pipe a child process's stdin/stdout.
func ServeStdio(ctx context.Context, srv *mcp.Server) error {
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// ServeHTTP runs the streamable HTTP transport until ctx is cancelled. It is
// proxy-friendly: stateless mode, JSON responses and DNS-rebind protection are
// all configurable so it can sit behind an MCP proxy or reverse proxy.
func ServeHTTP(ctx context.Context, srv *mcp.Server, cfg config.HTTPConfig, logger *slog.Logger) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless:                  cfg.Stateless,
			JSONResponse:               cfg.JSONResponse,
			DisableLocalhostProtection: cfg.DisableDNSRebindProtection,
			Logger:                     logger,
		},
	)

	mux := http.NewServeMux()
	mux.Handle(cfg.Path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("serving MCP over HTTP", "addr", cfg.Addr, "path", cfg.Path, "stateless", cfg.Stateless)
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
