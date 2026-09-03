// Command api serves the HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/config"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/storage"
	transport "github.com/mlkad/b2b-expense-tracker/internal/transport/http"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/handler"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
	"github.com/mlkad/b2b-expense-tracker/internal/worker"
)

// healthcheck runs the container probe instead of the server.
//
// The runtime image is built FROM scratch: no shell, no curl, nothing that a
// conventional `CMD-SHELL curl -f localhost/readyz` could use. The binary
// probes itself instead, which also means the probe and the server agree about
// which port and which path to use because both read the same configuration.
var healthcheck = flag.Bool("healthcheck", false,
	"probe the local readiness endpoint and exit 0 if it is healthy; for container health checks")

func main() {
	flag.Parse()

	if *healthcheck {
		if err := probe(); err != nil {
			fmt.Fprintf(os.Stderr, "unhealthy: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// main otherwise does nothing but call run and translate its error into an
	// exit code, so that every defer in run actually executes - os.Exit skips
	// them.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// probe asks the running server whether it is ready.
//
// It reads HTTP_ADDR so the probe follows a non-default port, and connects to
// the loopback interface rather than to the address the server binds - which is
// often ":8080", meaning every interface, and is not a thing a client can dial.
func probe() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("HTTP_ADDR %q is not host:port: %w", addr, err)
	}

	// Deliberately short. A probe that hangs is a container that is neither
	// healthy nor restarted, which is the worst of both.
	client := &http.Client{Timeout: 3 * time.Second}

	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The body is drained so the connection can be reused rather than reset,
	// which keeps a failing probe from filling the server's logs with EOFs.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness endpoint returned %d", resp.StatusCode)
	}
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.Log.Level, cfg.Log.Format, "expense-api", cfg.Version)
	slog.SetDefault(log)

	// Signals are trapped before anything is opened, so a Ctrl-C during
	// startup is handled rather than killing the process half way through
	// acquiring resources.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting",
		slog.String("env", cfg.Env),
		slog.String("version", cfg.Version),
		slog.String("database", cfg.RedactedDSN()))

	db, err := postgres.Open(ctx, postgres.Config{
		DSN:                       cfg.Database.DSN,
		MaxConns:                  cfg.Database.MaxConns,
		MinConns:                  cfg.Database.MinConns,
		StatementTimeout:          cfg.Database.StatementTimeout,
		IdleInTxTimeout:           cfg.Database.IdleInTxTimeout,
		SlowQueryThreshold:        cfg.Database.SlowQueryThreshold,
		ApplicationName:           "expense-api",
		VerifyTenantResetOnCommit: cfg.Database.VerifyTenantReset,
	}, log)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	tokens, err := auth.NewTokenService(auth.Config{
		Secret:   cfg.JWT.Secret,
		Issuer:   cfg.JWT.Issuer,
		Audience: cfg.JWT.Audience,
		TTL:      cfg.JWT.TTL,
	})
	if err != nil {
		return fmt.Errorf("tokens: %w", err)
	}

	var (
		gatewayClient *gateway.Client
		relay         *gateway.Relay
	)
	if cfg.Gateway.Enabled {
		gatewayClient, err = gateway.New(gateway.Config{
			BaseURL:       cfg.Gateway.BaseURL,
			ServiceSecret: cfg.Gateway.ServiceSecret,
			Issuer:        cfg.JWT.Issuer,
		})
		if err != nil {
			return fmt.Errorf("billing gateway: %w", err)
		}
		relay, err = gateway.NewRelay(cfg.Gateway.RelaySecret, gateway.DefaultTolerance)
		if err != nil {
			return fmt.Errorf("billing relay: %w", err)
		}
		log.Info("billing gateway configured", slog.String("url", cfg.Gateway.BaseURL))
	} else {
		// Explicitly announced. A deployment that silently runs without
		// billing gates every tenant onto the free tier, and nobody notices
		// until a customer asks why their plan does nothing.
		log.Warn("billing gateway is not configured; every tenant will resolve to the free plan")
	}

	// Receipts are optional: a deployment with no object store runs perfectly
	// well and answers 501 on the attachment endpoints. Announced at startup,
	// because a silently receipt-less deployment is discovered by a customer
	// rather than by an operator.
	var objectStore storage.Store
	if cfg.Storage.Enabled {
		s3, err := storage.NewS3Store(storage.S3Config{
			Endpoint:  cfg.Storage.Endpoint,
			Region:    cfg.Storage.Region,
			Bucket:    cfg.Storage.Bucket,
			AccessKey: cfg.Storage.AccessKey,
			SecretKey: cfg.Storage.SecretKey,
			PathStyle: cfg.Storage.PathStyle,
		})
		if err != nil {
			return fmt.Errorf("object storage: %w", err)
		}
		objectStore = s3
		log.Info("receipt storage configured",
			slog.String("endpoint", cfg.Storage.Endpoint),
			slog.String("bucket", cfg.Storage.Bucket))
	} else {
		log.Warn("no object storage configured; receipt uploads will be refused")
	}

	queue := worker.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer queue.Close()

	var (
		tenancyRepo = repo.NewTenancyRepository()
		expenseRepo = repo.NewExpenseRepository()
		billingRepo = repo.NewBillingRepository()
		budgetRepo  = repo.NewBudgetRepository()
		orgRepo     = repo.NewOrgRepository()
		fileRepo    = repo.NewAttachmentRepository()
	)

	scope := service.NewScope(db, tenancyRepo)

	var (
		authService    = service.NewAuthService(scope, tenancyRepo, tokens, cfg.JWT.RefreshTTL, log)
		expenseService = service.NewExpenseService(scope, expenseRepo, queue)
		billingService = service.NewBillingService(scope, billingRepo, tenancyRepo, gatewayClient, log)
		reportService  = service.NewReportService(scope, expenseRepo, billingRepo, tenancyRepo)
		orgService     = service.NewOrgService(scope, orgRepo, budgetRepo, tenancyRepo, billingRepo)
		fileService    = service.NewAttachmentService(scope, fileRepo, expenseRepo, objectStore,
			cfg.Storage.UploadTTL, cfg.Storage.DownloadTTL, log)
	)

	authLimiter := middleware.NewRateLimiter(0.5, 10) // ~30/min per address, burst 10
	writeLimiter := middleware.NewRateLimiter(20, 60) // per tenant
	limiterStop := make(chan struct{})
	defer close(limiterStop)
	go authLimiter.RunSweeper(limiterStop, time.Minute)
	go writeLimiter.RunSweeper(limiterStop, time.Minute)

	handlers := transport.Handlers{
		Auth:     handler.NewAuthHandler(authService, cfg.HTTP.TrustedProxies, cfg.HTTP.SecureCookies),
		Expenses: handler.NewExpenseHandler(expenseService),
		Exports:  handler.NewExportHandler(reportService, cfg.HTTP.ExportTimeout),
		Billing:  handler.NewBillingHandler(billingService, relay, log),
		Org:      handler.NewOrgHandler(orgService),
		Files:    handler.NewAttachmentHandler(fileService),
		Health:   handler.NewHealthHandler(db, cfg.Version),
	}

	router := transport.NewRouter(handlers, transport.RouterConfig{
		APITimeout:    cfg.HTTP.APITimeout,
		ExportTimeout: cfg.HTTP.ExportTimeout,
		RelayTimeout:  cfg.HTTP.RelayTimeout,
		CORS: middleware.CORSConfig{
			AllowedOrigins: cfg.HTTP.CORSOrigins,
		},
		Tokens:         tokens,
		AuthRateLimit:  authLimiter,
		WriteRateLimit: writeLimiter,
		TrustedProxies: cfg.HTTP.TrustedProxies,
	}, log)

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		// The server's error log would otherwise write unstructured lines to
		// stderr, which a JSON log pipeline drops on the floor.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext: func(_ net.Listener) context.Context {
			return context.Background()
		},
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", slog.String("addr", cfg.HTTP.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received", slog.Duration("grace", cfg.HTTP.ShutdownGrace))
	}

	// Graceful shutdown. Shutdown stops accepting new connections and waits
	// for in-flight requests, which for this service includes exports that may
	// have been streaming for minutes. The grace period is deliberately
	// shorter than the export timeout: a deploy should not be held up for ten
	// minutes by one report, and a truncated download is recoverable by
	// retrying it.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownGrace)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown timed out; closing connections", slog.String("error", err.Error()))
		if closeErr := server.Close(); closeErr != nil {
			return fmt.Errorf("force close: %w", closeErr)
		}
	}

	log.Info("stopped cleanly")
	return nil
}
