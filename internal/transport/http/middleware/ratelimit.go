package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is a token bucket per key, swept periodically.
//
// In-process and therefore per-replica: three replicas of this service allow
// three times the configured rate. That is a deliberate trade. A shared limiter
// in Redis is exact but puts a network round trip - and a dependency that can
// fail - in front of the login endpoint, and the failure mode of "Redis is
// down so nobody can log in" is worse than the failure mode of "the effective
// limit is 3x during an attack". The number is chosen with the replica count
// in mind.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	rate     float64 // tokens per second
	capacity float64
	ttl      time.Duration

	// now is injectable so the tests do not sleep. A rate limiter tested with
	// real time is a test that is either slow or flaky.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows `burst` requests immediately and refills at
// `perSecond`.
func NewRateLimiter(perSecond float64, burst int) *RateLimiter {
	if perSecond <= 0 {
		perSecond = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     perSecond,
		capacity: float64(burst),
		ttl:      10 * time.Minute,
		now:      time.Now,
	}
}

// Allow consumes a token for a key.
func (rl *RateLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := rl.now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, exists := rl.buckets[key]
	if !exists {
		// A new key starts full but immediately spends one token, so the
		// first request of a burst is not free.
		b = &bucket{tokens: rl.capacity, last: now}
		rl.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		b.tokens += elapsed * rl.rate
		if b.tokens > rl.capacity {
			b.tokens = rl.capacity
		}
		b.last = now
	}

	if b.tokens < 1 {
		deficit := 1 - b.tokens
		return false, time.Duration(deficit/rl.rate*float64(time.Second)) + time.Millisecond
	}
	b.tokens--
	return true, 0
}

// Sweep drops buckets nobody has touched recently.
//
// Without it the map is an unbounded allocation keyed by remote address, which
// is a memory exhaustion vector that costs an attacker one packet per entry.
func (rl *RateLimiter) Sweep() {
	now := rl.now()
	cutoff := now.Add(-rl.ttl)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, b := range rl.buckets {
		if !b.last.Before(cutoff) {
			continue
		}

		// The refill is projected forward rather than read from b.tokens.
		//
		// Tokens are only added inside Allow, so a key that was used once and
		// never again keeps whatever count it had at that moment - which for a
		// burst of one is zero, forever. Comparing the stored value against the
		// capacity therefore never matches, and the map grows without bound:
		// one request per address is enough to pin an entry for the lifetime of
		// the process, which is the memory exhaustion this sweep exists to
		// prevent.
		//
		// A bucket that would have refilled to capacity by now is
		// indistinguishable from one that never existed, so dropping it hands
		// the client nothing they did not already have.
		refilled := b.tokens + now.Sub(b.last).Seconds()*rl.rate
		if refilled >= rl.capacity {
			delete(rl.buckets, key)
		}
	}
}

// RunSweeper sweeps until the channel closes.
func (rl *RateLimiter) RunSweeper(stop <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			rl.Sweep()
		}
	}
}

// KeyFunc identifies the caller for limiting purposes.
type KeyFunc func(*http.Request) string

// ClientKeyFunc keys on the client address behind a known number of proxies.
func ClientKeyFunc(trustedProxies int) KeyFunc {
	return func(r *http.Request) string { return TrustedProxyIP(r, trustedProxies) }
}

// TenantKeyFunc keys on the authenticated tenant, for limits that should
// follow the customer rather than the connection - a tenant behind one office
// NAT must not be throttled as a single IP.
func TenantKeyFunc(fallback KeyFunc) KeyFunc {
	return func(r *http.Request) string {
		if s, err := SubjectFromContext(r.Context()); err == nil {
			return "tenant:" + s.TenantID.String()
		}
		return fallback(r)
	}
}

func (rl *RateLimiter) Middleware(key KeyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter := rl.Allow(key(r))
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
				writeProblem(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
