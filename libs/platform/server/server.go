// Package server runs HTTP servers and background loops until shutdown.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// New returns a server with timeouts suitable for a JSON API.
func New(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

// Run serves all servers and runs all loops until SIGINT/SIGTERM or until one
// of them fails. On shutdown, servers stop accepting connections and in-flight
// requests get up to grace to finish; loops get their context cancelled.
func Run(logger *slog.Logger, grace time.Duration, servers []*http.Server, loops ...func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g, gctx := errgroup.WithContext(ctx)
	for _, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			return err
		}
		logger.Info("listening", "addr", ln.Addr().String())
		g.Go(func() error {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
	}
	for _, loop := range loops {
		g.Go(func() error {
			if err := loop(gctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		logger.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		var errs []error
		for _, srv := range servers {
			errs = append(errs, srv.Shutdown(sctx))
		}
		return errors.Join(errs...)
	})
	return g.Wait()
}

// Env returns the environment variable or a default.
func Env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// EnvList splits a comma-separated environment variable.
func EnvList(key string) []string {
	var out []string
	for part := range strings.SplitSeq(os.Getenv(key), ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// EnvSecret reads a secret from KEY, or from the file named by KEY_FILE.
// Kubernetes mounts secrets as files, which keeps them out of the process
// environment and of "kubectl describe pod".
func EnvSecret(key string) (string, error) {
	if path := os.Getenv(key + "_FILE"); path != "" {
		// The path is deployment configuration set by the operator in the
		// manifest, never request data. Reviewed exception, see docs/security/exceptions.md.
		b, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 G703 -- operator-controlled path
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := os.Getenv(key); v != "" {
		return v, nil
	}
	return "", errors.New(key + " or " + key + "_FILE must be set")
}
