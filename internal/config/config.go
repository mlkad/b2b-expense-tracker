// Package config reads the process configuration from the environment.
//
// Every value is read once, at startup, and validated there. A service that
// discovers a missing secret on the first request that needs it has already
// told its orchestrator it is healthy, and the failure surfaces as a 500 to a
// customer rather than as a deployment that refuses to roll.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
)

type Config struct {
	Env     string
	Version string

	HTTP     HTTPConfig
	Database DatabaseConfig
	Redis    RedisConfig
	JWT      JWTConfig
	Gateway  GatewayConfig
	Log      LogConfig
}

type HTTPConfig struct {
	Addr           string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	ShutdownGrace  time.Duration
	APITimeout     time.Duration
	ExportTimeout  time.Duration
	RelayTimeout   time.Duration
	CORSOrigins    []string
	TrustedProxies int
	SecureCookies  bool
}

type DatabaseConfig struct {
	DSN                string
	MaxConns           int32
	MinConns           int32
	StatementTimeout   time.Duration
	IdleInTxTimeout    time.Duration
	SlowQueryThreshold time.Duration
	VerifyTenantReset  bool
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type JWTConfig struct {
	Secret     string
	Issuer     string
	Audience   string
	TTL        time.Duration
	RefreshTTL time.Duration
}

type GatewayConfig struct {
	BaseURL       string
	ServiceSecret string
	RelaySecret   string
	Enabled       bool
}

type LogConfig struct {
	Level  slog.Level
	Format logger.Format
}

// Load reads and validates the environment.
//
// Errors accumulate rather than returning on the first one. Someone bringing
// up a new environment should see every missing variable at once, not
// rediscover them one deployment at a time.
func Load() (*Config, error) {
	var problems []string

	cfg := &Config{
		Env:     env("APP_ENV", "development"),
		Version: env("APP_VERSION", "dev"),

		HTTP: HTTPConfig{
			Addr: env("HTTP_ADDR", ":8080"),
			// ReadTimeout covers headers and body. Slowloris is a request that
			// sends one byte a second forever, and it costs nothing to defeat.
			ReadTimeout: duration("HTTP_READ_TIMEOUT", 15*time.Second, &problems),
			// WriteTimeout has to exceed the longest export, or a large report
			// is cut off by the server rather than by its own deadline.
			WriteTimeout:   duration("HTTP_WRITE_TIMEOUT", 15*time.Minute, &problems),
			IdleTimeout:    duration("HTTP_IDLE_TIMEOUT", 120*time.Second, &problems),
			ShutdownGrace:  duration("HTTP_SHUTDOWN_GRACE", 20*time.Second, &problems),
			APITimeout:     duration("HTTP_API_TIMEOUT", 10*time.Second, &problems),
			ExportTimeout:  duration("HTTP_EXPORT_TIMEOUT", 10*time.Minute, &problems),
			RelayTimeout:   duration("HTTP_RELAY_TIMEOUT", 25*time.Second, &problems),
			CORSOrigins:    list("CORS_ALLOWED_ORIGINS"),
			TrustedProxies: integer("TRUSTED_PROXY_HOPS", 0, &problems),
			SecureCookies:  boolean("SECURE_COOKIES", true),
		},

		Database: DatabaseConfig{
			DSN:                os.Getenv("DATABASE_URL"),
			MaxConns:           int32(integer("DB_MAX_CONNS", 25, &problems)),
			MinConns:           int32(integer("DB_MIN_CONNS", 2, &problems)),
			StatementTimeout:   duration("DB_STATEMENT_TIMEOUT", 15*time.Second, &problems),
			IdleInTxTimeout:    duration("DB_IDLE_IN_TX_TIMEOUT", 30*time.Second, &problems),
			SlowQueryThreshold: duration("DB_SLOW_QUERY_THRESHOLD", 250*time.Millisecond, &problems),
			VerifyTenantReset:  boolean("DB_VERIFY_TENANT_RESET", false),
		},

		Redis: RedisConfig{
			Addr:     env("REDIS_ADDR", "127.0.0.1:6390"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       integer("REDIS_DB", 0, &problems),
		},

		JWT: JWTConfig{
			Secret:     os.Getenv("JWT_SECRET"),
			Issuer:     env("JWT_ISSUER", "b2b-expense-tracker"),
			Audience:   env("JWT_AUDIENCE", "b2b-expense-tracker-api"),
			TTL:        duration("JWT_TTL", 15*time.Minute, &problems),
			RefreshTTL: duration("REFRESH_TTL", 30*24*time.Hour, &problems),
		},

		Gateway: GatewayConfig{
			BaseURL:       os.Getenv("BILLING_GATEWAY_URL"),
			ServiceSecret: os.Getenv("BILLING_SERVICE_SECRET"),
			RelaySecret:   os.Getenv("BILLING_RELAY_SECRET"),
		},

		Log: LogConfig{
			Level:  logger.ParseLevel(env("LOG_LEVEL", "info")),
			Format: logger.Format(env("LOG_FORMAT", "json")),
		},
	}

	cfg.Gateway.Enabled = cfg.Gateway.BaseURL != ""

	if cfg.Database.DSN == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if len(cfg.JWT.Secret) < auth.MinSecretBytes {
		problems = append(problems, fmt.Sprintf("JWT_SECRET must be at least %d bytes", auth.MinSecretBytes))
	}
	if cfg.Gateway.Enabled {
		if len(cfg.Gateway.ServiceSecret) < 32 {
			problems = append(problems, "BILLING_SERVICE_SECRET must be at least 32 bytes when BILLING_GATEWAY_URL is set")
		}
		if len(cfg.Gateway.RelaySecret) < 32 {
			problems = append(problems, "BILLING_RELAY_SECRET must be at least 32 bytes when BILLING_GATEWAY_URL is set")
		}
	}

	// The checks that only matter in production, where getting them wrong is
	// not visible until something has already gone wrong.
	if cfg.IsProduction() {
		if !cfg.HTTP.SecureCookies {
			problems = append(problems, "SECURE_COOKIES must not be disabled in production")
		}
		if len(cfg.HTTP.CORSOrigins) == 0 {
			problems = append(problems, "CORS_ALLOWED_ORIGINS must list the dashboard origin in production")
		}
		for _, origin := range cfg.HTTP.CORSOrigins {
			if origin == "*" {
				problems = append(problems, "CORS_ALLOWED_ORIGINS must not contain '*': the API sends credentials")
			}
		}
		if strings.Contains(cfg.Database.DSN, "sslmode=disable") {
			problems = append(problems, "DATABASE_URL must not disable TLS in production")
		}
	}

	// The write timeout has to outlast the longest thing written. Getting this
	// backwards produces a truncated download at exactly the size where
	// somebody notices it in a customer report.
	if cfg.HTTP.WriteTimeout > 0 && cfg.HTTP.WriteTimeout <= cfg.HTTP.ExportTimeout {
		problems = append(problems, fmt.Sprintf(
			"HTTP_WRITE_TIMEOUT (%s) must exceed HTTP_EXPORT_TIMEOUT (%s), or large exports are cut off mid-stream",
			cfg.HTTP.WriteTimeout, cfg.HTTP.ExportTimeout))
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("configuration is invalid:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func (c *Config) IsProduction() bool {
	return c.Env == "production" || c.Env == "prod"
}

// RedactedDSN is safe to log. A connection string in a log line is a
// credential in a log line, and log lines end up in tickets.
func (c *Config) RedactedDSN() string {
	dsn := c.Database.DSN
	at := strings.LastIndex(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return "postgres://[redacted]"
	}
	return dsn[:scheme+3] + "[redacted]" + dsn[at:]
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func list(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// duration records a malformed value as a problem rather than silently using
// the default. A typo in a timeout should stop the deployment, not produce a
// service running with a timeout nobody chose.
func duration(key string, fallback time.Duration, problems *[]string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s is not a duration (e.g. 30s, 5m): %v", key, err))
		return fallback
	}
	if d <= 0 {
		*problems = append(*problems, key+" must be positive")
		return fallback
	}
	return d
}

func integer(key string, fallback int, problems *[]string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s is not a number: %v", key, err))
		return fallback
	}
	return n
}

func boolean(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}
