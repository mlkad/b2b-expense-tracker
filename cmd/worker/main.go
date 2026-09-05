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
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/hibiken/asynq"

	"github.com/mlkad/b2b-expense-tracker/internal/config"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/notify"
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

// runTask enqueues one sweep and exits, instead of starting the server.
//
// The scheduled sweeps otherwise run only on a cron - the recurring charge
// sweep at 02:15, reconciliation at 03:40 - which makes them impossible to
// exercise deliberately, whether during an incident or while checking that
// they work at all. This is the same enqueue the scheduler performs, not a
// second path that could behave differently.
var runTask = flag.String("task", "",
	"enqueue one sweep and exit: "+strings.Join(worker.PeriodicTasks, ", "))

func run() error {
	flag.Parse()

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
		orgRepo     = repo.NewOrgRepository()
	)
	scope := service.NewScope(db, tenancyRepo)
	billingService := service.NewBillingService(scope, billingRepo, tenancyRepo, gatewayClient, log)

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}

	// The worker is also a producer: the reconciliation sweep fans out into one
	// job per tenant.
	queue := worker.NewClient(redisOpt)
	defer queue.Close()

	notifier, err := buildNotifier(cfg, log)
	if err != nil {
		return fmt.Errorf("notifications: %w", err)
	}

	handlers := worker.NewHandlers(db, expenseRepo, budgetRepo, tenancyRepo, orgRepo,
		billingService, queue, notifier, log)

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
	//
	// The relay sweep runs often because what it recovers is invisible: a
	// delivery stuck in 'processing' blocks every redelivery of that event, so
	// the subscription silently stops updating and nothing reports it. The
	// others are daily, staggered so they do not contend for the same
	// connections.
	periodic := []struct {
		cron  string
		task  string
		queue string
	}{
		{"*/10 * * * *", worker.TaskRelaySweep, worker.QueueDefault},
		{"15 2 * * *", worker.TaskRecurringSweep, worker.QueueDefault},
		{"40 3 * * *", worker.TaskBillingReconcileSweep, worker.QueueLow},
		{"20 4 * * *", worker.TaskSessionCleanup, worker.QueueLow},
	}
	// A sweep listed for -task but never scheduled would silently never run;
	// one scheduled but not listed could not be triggered during an incident.
	if len(periodic) != len(worker.PeriodicTasks) {
		return fmt.Errorf("%d sweeps are scheduled but %d are listed in worker.PeriodicTasks",
			len(periodic), len(worker.PeriodicTasks))
	}
	for _, p := range periodic {
		if _, err := scheduler.Register(p.cron,
			asynq.NewTask(p.task, nil),
			asynq.Queue(p.queue),
			// A lease that expires, not an identifier that is held.
			//
			// asynq.TaskID would also stop two copies overlapping, and would
			// keep stopping them after the task was archived - so a sweep that
			// failed permanently could never run again, silently. Unique takes
			// a lock for a bounded time instead, so a failure costs one
			// interval rather than every future one.
			asynq.Unique(uniqueFor(p.cron)),
		); err != nil {
			return fmt.Errorf("schedule %s: %w", p.task, err)
		}
		log.Info("scheduled", slog.String("task", p.task), slog.String("cron", p.cron))
	}

	go func() {
		if err := scheduler.Run(); err != nil {
			log.Error("scheduler stopped", slog.String("error", err.Error()))
		}
	}()

	if *runTask != "" {
		if !slices.Contains(worker.PeriodicTasks, *runTask) {
			return fmt.Errorf("unknown task %q; known sweeps are %s",
				*runTask, strings.Join(worker.PeriodicTasks, ", "))
		}
		if err := queue.EnqueuePeriodic(ctx, *runTask); err != nil {
			return fmt.Errorf("enqueue %s: %w", *runTask, err)
		}
		log.Info("sweep enqueued; a running worker will pick it up",
			slog.String("task", *runTask))
		return nil
	}

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

// uniqueFor is how long a sweep holds its overlap lock.
//
// Comfortably under the interval between runs, so the lock is gone before the
// next tick and a run is never skipped because the previous lease outlived it.
// The relay sweep runs every ten minutes; the rest are daily.
func uniqueFor(cron string) time.Duration {
	if strings.HasPrefix(cron, "*/10") {
		return 8 * time.Minute
	}
	return time.Hour
}

// buildNotifier wires the mail relay, or a logging stand-in when there is none.
//
// The stand-in is deliberately not a silent discard. An operator reading the
// log can see notifications being produced and where they would have gone,
// which is the difference between "mail is not configured" and "the
// notification code is broken".
func buildNotifier(cfg *config.Config, log *slog.Logger) (worker.Notifier, error) {
	var sender notify.Sender

	if cfg.Mail.Enabled {
		smtpSender, err := notify.NewSMTPSender(notify.SMTPConfig{
			Host:     cfg.Mail.Host,
			Port:     cfg.Mail.Port,
			Username: cfg.Mail.Username,
			Password: cfg.Mail.Password,
			From:     notify.Recipient{Name: cfg.Mail.FromName, Email: cfg.Mail.FromAddr},
			TLS:      notify.TLSMode(cfg.Mail.TLS),
		})
		if err != nil {
			return nil, err
		}
		sender = smtpSender
		log.Info("mail relay configured",
			slog.String("host", cfg.Mail.Host),
			slog.Int("port", cfg.Mail.Port),
			slog.String("tls", cfg.Mail.TLS))
	} else {
		log.Warn("no mail relay configured; notifications will be logged instead of sent")
		sender = notify.LoggingSender{Log: func(ctx context.Context, m notify.Message) {
			// Recipient count rather than addresses. The addresses are already
			// in the database, and a log aggregator is a much wider audience.
			log.InfoContext(ctx, "notification not sent (no relay configured)",
				slog.String("category", m.Category),
				slog.String("subject", m.Subject),
				slog.Int("recipients", len(m.To)))
		}}
	}

	return notify.New(sender, cfg.Mail.DashboardURL)
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
