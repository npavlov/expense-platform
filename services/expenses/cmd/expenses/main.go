// Command expenses runs the expense submission and approval service and its
// outbox delivery worker.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/npavlov/expense-platform/libs/platform/authn"
	"github.com/npavlov/expense-platform/libs/platform/httpx"
	"github.com/npavlov/expense-platform/libs/platform/logging"
	"github.com/npavlov/expense-platform/libs/platform/server"
	"github.com/npavlov/expense-platform/libs/platform/sigauth"
	"github.com/npavlov/expense-platform/services/expenses/internal/claims"
	"github.com/npavlov/expense-platform/services/expenses/internal/httpapi"
	"github.com/npavlov/expense-platform/services/expenses/internal/outbox"
	"github.com/npavlov/expense-platform/services/expenses/internal/storage"
)

func main() {
	logger := logging.New("expenses")
	if err := run(logger); err != nil {
		logger.Error("service stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn, err := server.EnvSecret("DATABASE_URL")
	if err != nil {
		return err
	}
	pool, err := storage.Open(ctx, dsn, "expenses")
	if err != nil {
		return err
	}
	defer pool.Close()

	insecure, _ := strconv.ParseBool(os.Getenv("AUTH_ALLOW_INSECURE_ISSUER"))
	verifier, err := authn.NewVerifier(ctx, authn.Config{
		Issuer:              os.Getenv("AUTH_ISSUER"),
		JWKSURL:             os.Getenv("AUTH_JWKS_URL"),
		ClientIDs:           server.EnvList("AUTH_CLIENT_IDS"),
		AllowInsecureIssuer: insecure,
	})
	if err != nil {
		return err
	}
	if insecure {
		logger.Warn("AUTH_ALLOW_INSECURE_ISSUER is set: accepting an http issuer (local development only)")
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	claimStore := storage.NewClaims(pool)
	outboxStore := storage.NewOutbox(pool)

	public := httpapi.NewRouter(httpapi.Deps{
		Claims:   claims.NewService(claimStore),
		Verifier: verifier,
		Ready:    func(ctx context.Context) error { return storage.Ready(ctx, pool) },
		Log:      logger,
	})

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	adminMux := http.NewServeMux()
	outbox.AdminRoutes(adminMux, outboxStore, logger)

	servers := []*http.Server{
		server.New(server.Env("HTTP_ADDR", ":8080"), httpx.Wrap(public, logger, httpx.NewMetrics(reg, "public"))),
		server.New(server.Env("METRICS_ADDR", ":9090"), metricsMux),
		// Bound to loopback: reachable only via kubectl port-forward.
		server.New(server.Env("ADMIN_ADDR", "127.0.0.1:9091"), httpx.Wrap(adminMux, logger, nil)),
	}

	om := outbox.NewMetrics(reg)
	loops := []func(context.Context) error{
		func(ctx context.Context) error { return outbox.RunStats(ctx, outboxStore, om, 5*time.Second, logger) },
	}
	if enabled, _ := strconv.ParseBool(server.Env("OUTBOX_WORKER_ENABLED", "true")); enabled {
		worker, err := newWorker(outboxStore, om, logger)
		if err != nil {
			return err
		}
		loops = append(loops, worker.Run)
	}

	return server.Run(logger, 10*time.Second, servers, loops...)
}

func newWorker(store outbox.Store, m *outbox.Metrics, logger *slog.Logger) (*outbox.Worker, error) {
	url := os.Getenv("AUDIT_EVENTS_URL")
	if url == "" {
		return nil, errors.New("AUDIT_EVENTS_URL must be set when the outbox worker is enabled")
	}
	key, err := server.EnvSecret("AUDIT_SIGNING_KEY")
	if err != nil {
		return nil, err
	}
	if len(key) < 32 {
		return nil, errors.New("AUDIT_SIGNING_KEY must be at least 32 bytes")
	}
	signer := sigauth.Signer{KeyID: server.Env("AUDIT_SIGNING_KEY_ID", "k1"), Key: []byte(key)}

	cfg := outbox.DefaultConfig()
	for name, dst := range map[string]*time.Duration{
		"OUTBOX_POLL_INTERVAL": &cfg.PollInterval,
		"OUTBOX_BACKOFF_BASE":  &cfg.BackoffBase,
		"OUTBOX_BACKOFF_MAX":   &cfg.BackoffMax,
	} {
		if v := os.Getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			*dst = d
		}
	}
	if v := os.Getenv("OUTBOX_MAX_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("OUTBOX_MAX_ATTEMPTS: %w", err)
		}
		cfg.MaxAttempts = n
	}
	return outbox.NewWorker(store, outbox.NewHTTPDeliverer(url, signer), cfg, logger, m), nil
}
