// Command worker runs the background jobs.
//
// It is a separate binary from the API rather than a goroutine inside it, for
// one reason that matters operationally: the two have different failure modes
// and different scaling needs. A backlog of report generation should be
// answered by adding worker replicas, not by adding API replicas that each
// bring their own connection pool; and a worker that runs out of memory
// processing a large job should not take the API down with it.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"

	"github.com/mlkad/b2b-expense-tracker/internal/config"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.Log.Level, cfg.Log.Format, "expense-worker", cfg.Version)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, postgres.Config{
		DSN: cfg.Database.DSN,
		// A worker needs far fewer connections than the API: its concurrency
		// is bounded by the Asynq worker count, not by inbound requests. Every
		// connection here is one the API cannot have.
		MaxConns:                  8,
		MinConns:                  1,
		StatementTimeout:          2 * time.Minute, // sweeps legitimately run long
		IdleInTxTimeout:           cfg.Database.IdleInTxTimeout,
		SlowQueryThreshold:        cfg.Database.SlowQueryThreshold,
		ApplicationName:           "expense-worker",
		VerifyTenantResetOnCommit: cfg.Database.VerifyTenantReset,
	}, log)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	var gatewayClient *gateway.Client
	if cfg.Gateway.Enabled {
		gatewayClient, err = gateway.New(gateway.Config{
			BaseURL:       cfg.Gateway.BaseURL,
			ServiceSecret: cfg.Gateway.ServiceSecret,
			Issuer:        cfg.JWT.Issuer,
		})
		if err != nil {
			return fmt.Errorf("billing gateway: %w", err)
		}
	}

	var (
		tenancyRepo = repo.NewTenancyRepository()
		expenseRepo = repo.NewExpenseRepository()
		budgetRepo  = repo.NewBudgetRepository()
		billingRepo = repo.NewBillingRepository()
	)
	scope := service.NewScope(db, tenancyRepo)
	billingService := service.NewBillingService(scope, billingRepo, tenancyRepo, gatewayClient, log)

	// The notifier is nil in this build. Every job that would send a message
	// logs what it would have sent instead, so the scheduling, deduplication
	// and retry behaviour is exercisable end to end without an email provider.
	handlers := worker.NewHandlers(db, expenseRepo, budgetRepo, billingService, nil, log)

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}

	server := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: 10,
		Queues:      worker.QueuePriorities,
		// StrictPriority is off: with it, a permanent backlog on `critical`
		// would starve the other queues entirely. Weighted draw means low
		// priority work still progresses during a busy period, just slowly.
		StrictPriority: false,

		// Exponential backoff with a ceiling. The default doubles without
		// limit, which for a job with 25 retries schedules the last attempt
		// weeks out - long after anyone would care about the result.
		RetryDelayFunc: asynq.RetryDelayFunc(func(n int, _ error, _ *asynq.Task) time.Duration {
			delay := time.Duration(1<<uint(min(n, 6))) * time.Second
			return min(delay, 10*time.Minute)
		}),

		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			retried, _ := asynq.GetRetryCount(ctx)
			maxRetry, _ := asynq.GetMaxRetry(ctx)

			// The last failure is the one that matters: it means the job is
			// being archived and the work will not happen unless somebody
			// acts. Earlier failures are expected noise.
			level := slog.LevelWarn
			if retried >= maxRetry {
				level = slog.LevelError
			}
			log.LogAttrs(ctx, level, "task failed",
				slog.String("type", task.Type()),
				slog.Int("retry", retried),
				slog.Int("max_retry", maxRetry),
				slog.String("error", err.Error()))
		}),

		Logger: asynqLogger{log},
	})

	mux := asynq.NewServeMux()
	handlers.Register(mux)

	// The periodic jobs. Times are UTC and are chosen to sit outside the
	// working day of the customer base's main time zones.
	scheduler := asynq.NewScheduler(redisOpt, &asynq.SchedulerOpts{
		Location: time.UTC,
		Logger:   asynqLogger{log},
	})
	if _, err := scheduler.Register("15 2 * * *",
		asynq.NewTask(worker.TaskRecurringSweep, nil),
		asynq.Queue(worker.QueueDefault)); err != nil {
		return fmt.Errorf("schedule recurring sweep: %w", err)
	}

	go func() {
		if err := scheduler.Run(); err != nil {
			log.Error("scheduler stopped", slog.String("error", err.Error()))
		}
	}()

	log.Info("worker starting", slog.String("redis", cfg.Redis.Addr))

	serverErr := make(chan error, 1)
	go func() {
		if err := server.Run(mux); err != nil {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("worker server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	scheduler.Shutdown()
	// Asynq's Shutdown waits for in-flight tasks to finish, then requeues
	// nothing: a job that was mid-flight when the process stopped is returned
	// to the queue by the server's own lease expiry, which is why every
	// handler has to be idempotent.
	server.Shutdown()

	log.Info("worker stopped cleanly")
	return nil
}

// asynqLogger adapts slog to the interface Asynq expects, so the worker's
// output is one stream in one format rather than two.
type asynqLogger struct{ log *slog.Logger }

func (l asynqLogger) Debug(args ...any) { l.log.Debug(fmt.Sprint(args...)) }
func (l asynqLogger) Info(args ...any)  { l.log.Info(fmt.Sprint(args...)) }
func (l asynqLogger) Warn(args ...any)  { l.log.Warn(fmt.Sprint(args...)) }
func (l asynqLogger) Error(args ...any) { l.log.Error(fmt.Sprint(args...)) }
func (l asynqLogger) Fatal(args ...any) {
	l.log.Error(fmt.Sprint(args...))
	os.Exit(1)
}
