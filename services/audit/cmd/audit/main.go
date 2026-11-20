// Command audit runs the decision history service.
package main

import (
	"context"
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
	"github.com/npavlov/expense-platform/services/audit/internal/httpapi"
	"github.com/npavlov/expense-platform/services/audit/internal/storage"
)

func main() {
	logger := logging.New("audit")
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
	pool, err := storage.Open(ctx, dsn, "audit")
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
	rawKeys, err := server.EnvSecret("INGEST_SIGNING_KEYS")
	if err != nil {
		return err
	}
	keys, err := sigauth.ParseKeys(rawKeys)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	deps := httpapi.Deps{
		Store:     storage.NewDecisions(pool),
		Verifier:  verifier,
		Signature: sigauth.Verifier{Keys: keys, MaxSkew: 5 * time.Minute},
		Ready:     func(ctx context.Context) error { return storage.Ready(ctx, pool) },
		Log:       logger,
		Metrics:   httpapi.NewMetrics(reg),
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	return server.Run(logger, 10*time.Second, []*http.Server{
		server.New(server.Env("HTTP_ADDR", ":8080"), httpx.Wrap(httpapi.NewPublicRouter(deps), logger, httpx.NewMetrics(reg, "public"))),
		server.New(server.Env("INTERNAL_ADDR", ":8081"), httpx.Wrap(httpapi.NewInternalRouter(deps), logger, httpx.NewMetrics(reg, "internal"))),
		server.New(server.Env("METRICS_ADDR", ":9090"), metricsMux),
	})
}
