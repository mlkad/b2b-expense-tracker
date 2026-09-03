package config

import (
	"strings"
	"testing"
	"time"
)

// valid is the smallest environment that starts. Each test copies it and
// breaks exactly one thing, so a failure names the thing that was broken.
func valid() map[string]string {
	return map[string]string{
		"APP_ENV":              "development",
		"DATABASE_URL":         "postgres://expense_app:pw@127.0.0.1:5441/expenses?sslmode=disable",
		"JWT_SECRET":           strings.Repeat("k", 48),
		"CORS_ALLOWED_ORIGINS": "http://localhost:5173",
		"SECURE_COOKIES":       "false",
	}
}

func load(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

func TestLoadAcceptsAMinimalDevelopmentEnvironment(t *testing.T) {
	cfg, err := load(t, valid())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Addr != ":8080" || cfg.JWT.TTL != 15*time.Minute {
		t.Fatalf("defaults not applied: %+v", cfg.HTTP)
	}
	if cfg.Gateway.Enabled {
		t.Error("billing reported as enabled with no gateway url")
	}
}

// Every problem is reported at once. Someone bringing up a new environment
// should not rediscover them one deployment at a time.
func TestLoadReportsEveryProblemTogether(t *testing.T) {
	env := valid()
	delete(env, "DATABASE_URL")
	env["JWT_SECRET"] = "too-short"
	env["JWT_TTL"] = "fifteen minutes"

	_, err := load(t, env)
	if err == nil {
		t.Fatal("an invalid configuration started")
	}
	for _, want := range []string{"DATABASE_URL", "JWT_SECRET", "JWT_TTL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %s:\n%v", want, err)
		}
	}
}

// A typo in a duration must stop the deployment, not silently produce a
// service running with a timeout nobody chose.
func TestMalformedDurationIsAnErrorNotADefault(t *testing.T) {
	env := valid()
	env["HTTP_API_TIMEOUT"] = "10 seconds"

	if _, err := load(t, env); err == nil {
		t.Fatal("a malformed duration was silently replaced by the default")
	}

	env["HTTP_API_TIMEOUT"] = "0s"
	if _, err := load(t, env); err == nil {
		t.Fatal("a zero timeout was accepted")
	}
}

// Getting this backwards truncates large downloads at exactly the size where
// somebody notices it in a customer report.
func TestWriteTimeoutMustOutlastTheExportTimeout(t *testing.T) {
	env := valid()
	env["HTTP_WRITE_TIMEOUT"] = "30s"
	env["HTTP_EXPORT_TIMEOUT"] = "10m"

	_, err := load(t, env)
	if err == nil {
		t.Fatal("a write timeout shorter than the export timeout was accepted")
	}
	if !strings.Contains(err.Error(), "cut off mid-stream") {
		t.Errorf("the error does not explain the consequence:\n%v", err)
	}
}

// The checks that only matter where getting them wrong is invisible until
// something has already gone wrong.
func TestProductionRefusesUnsafeSettings(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]string)
		want   string
	}{
		"insecure cookies": {
			func(e map[string]string) { e["SECURE_COOKIES"] = "false" },
			"SECURE_COOKIES",
		},
		"no cors origins": {
			func(e map[string]string) { delete(e, "CORS_ALLOWED_ORIGINS") },
			"CORS_ALLOWED_ORIGINS",
		},
		"wildcard cors": {
			func(e map[string]string) { e["CORS_ALLOWED_ORIGINS"] = "*" },
			"must not contain",
		},
		"tls disabled on the database": {
			func(e map[string]string) {
				e["DATABASE_URL"] = "postgres://u:p@db/expenses?sslmode=disable"
			},
			"TLS",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env := valid()
			env["APP_ENV"] = "production"
			env["SECURE_COOKIES"] = "true"
			env["DATABASE_URL"] = "postgres://u:p@db/expenses?sslmode=require"
			c.mutate(env)

			_, err := load(t, env)
			if err == nil {
				t.Fatalf("production accepted %s", name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error does not mention %q:\n%v", c.want, err)
			}
		})
	}

	t.Run("a correct production environment starts", func(t *testing.T) {
		env := valid()
		env["APP_ENV"] = "production"
		env["SECURE_COOKIES"] = "true"
		env["DATABASE_URL"] = "postgres://u:p@db/expenses?sslmode=require"
		env["CORS_ALLOWED_ORIGINS"] = "https://app.example.com"

		cfg, err := load(t, env)
		if err != nil {
			t.Fatalf("a valid production environment was refused: %v", err)
		}
		if !cfg.IsProduction() {
			t.Error("IsProduction is false for APP_ENV=production")
		}
	})
}

// Billing is all-or-nothing: a gateway url without its secrets would start and
// then fail on the first checkout.
func TestGatewaySecretsAreRequiredOnceTheUrlIsSet(t *testing.T) {
	env := valid()
	env["BILLING_GATEWAY_URL"] = "https://billing.internal"

	_, err := load(t, env)
	if err == nil {
		t.Fatal("a gateway url with no secrets was accepted")
	}
	for _, want := range []string{"BILLING_SERVICE_SECRET", "BILLING_RELAY_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}

	env["BILLING_SERVICE_SECRET"] = strings.Repeat("s", 40)
	env["BILLING_RELAY_SECRET"] = strings.Repeat("r", 40)
	cfg, err := load(t, env)
	if err != nil {
		t.Fatalf("a complete billing configuration was refused: %v", err)
	}
	if !cfg.Gateway.Enabled {
		t.Error("billing not marked enabled")
	}
}

// A connection string in a log line is a credential in a log line, and log
// lines end up in tickets.
func TestRedactedDSNHidesTheCredential(t *testing.T) {
	cfg, err := load(t, valid())
	if err != nil {
		t.Fatal(err)
	}

	redacted := cfg.RedactedDSN()
	if strings.Contains(redacted, "pw") {
		t.Fatalf("the password survived redaction: %s", redacted)
	}
	if !strings.Contains(redacted, "5441/expenses") {
		t.Errorf("redaction removed the host, which is what makes the line useful: %s", redacted)
	}

	t.Run("an unparseable dsn redacts entirely rather than leaking", func(t *testing.T) {
		c := &Config{Database: DatabaseConfig{DSN: "host=db password=hunter2 user=app"}}
		if strings.Contains(c.RedactedDSN(), "hunter2") {
			t.Fatalf("a keyword/value dsn leaked its password: %s", c.RedactedDSN())
		}
	})
}

func TestCORSOriginListIsTrimmedAndSplit(t *testing.T) {
	env := valid()
	env["CORS_ALLOWED_ORIGINS"] = " https://a.example.com , https://b.example.com ,, "

	cfg, err := load(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTP.CORSOrigins) != 2 {
		t.Fatalf("parsed %v, want two origins with the blanks dropped", cfg.HTTP.CORSOrigins)
	}
	if cfg.HTTP.CORSOrigins[0] != "https://a.example.com" {
		t.Errorf("whitespace survived: %q", cfg.HTTP.CORSOrigins[0])
	}
}
