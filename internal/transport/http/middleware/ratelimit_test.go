package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A rate limiter tested against the wall clock is either slow or flaky, so the
// clock is injected.
func fixedClock(rl *RateLimiter) func(time.Duration) {
	now := time.Now()
	rl.now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

func TestBurstThenRefill(t *testing.T) {
	rl := NewRateLimiter(2, 3) // 2/sec, burst 3
	advance := fixedClock(rl)

	for i := 0; i < 3; i++ {
		if ok, _ := rl.Allow("client"); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}

	ok, retryAfter := rl.Allow("client")
	if ok {
		t.Fatal("the bucket allowed a fourth request with no time elapsed")
	}
	if retryAfter <= 0 {
		t.Error("a refusal must say how long to wait, or a client retries immediately")
	}

	// Half a second at two per second is one token.
	advance(500 * time.Millisecond)
	if ok, _ := rl.Allow("client"); !ok {
		t.Fatal("the bucket did not refill")
	}
	if ok, _ := rl.Allow("client"); ok {
		t.Fatal("the bucket refilled by more than the elapsed time allows")
	}
}

func TestBucketDoesNotRefillPastCapacity(t *testing.T) {
	rl := NewRateLimiter(10, 2)
	advance := fixedClock(rl)

	rl.Allow("client")
	advance(time.Hour) // would be 36,000 tokens without a cap

	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := rl.Allow("client"); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("allowed %d after an hour idle, want the burst of 2: an uncapped bucket "+
			"lets a client save up an unlimited allowance", allowed)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	fixedClock(rl)

	if ok, _ := rl.Allow("tenant-a"); !ok {
		t.Fatal("first client refused")
	}
	if ok, _ := rl.Allow("tenant-a"); ok {
		t.Fatal("first client was allowed twice")
	}
	if ok, _ := rl.Allow("tenant-b"); !ok {
		t.Fatal("a second client was throttled by the first one's usage")
	}
}

// The map is keyed by remote address, so without a sweep it is an unbounded
// allocation that costs an attacker one packet per entry.
func TestSweepReleasesIdleBuckets(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	advance := fixedClock(rl)

	for i := 0; i < 500; i++ {
		rl.Allow("attacker-" + strconv.Itoa(i))
	}
	if len(rl.buckets) != 500 {
		t.Fatalf("holding %d buckets, want 500", len(rl.buckets))
	}

	// Not yet stale.
	rl.Sweep()
	if len(rl.buckets) != 500 {
		t.Fatalf("swept %d live buckets", 500-len(rl.buckets))
	}

	advance(rl.ttl + time.Minute)
	rl.Sweep()
	if len(rl.buckets) != 0 {
		t.Fatalf("%d buckets survived the sweep", len(rl.buckets))
	}
}

// A bucket that is stale but still has requests owing must not be dropped -
// deleting it would hand the client a fresh full burst.
func TestSweepKeepsBucketsThatStillOweTime(t *testing.T) {
	rl := NewRateLimiter(0.001, 1) // refills very slowly
	advance := fixedClock(rl)

	rl.Allow("client") // spends the only token
	advance(rl.ttl + time.Minute)
	rl.Sweep()

	if len(rl.buckets) != 1 {
		t.Fatal("a throttled client's bucket was swept; they would get a fresh burst immediately")
	}
}

func TestMiddlewareAnswers429WithRetryAfter(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	fixedClock(rl)

	handler := rl.Middleware(func(*http.Request) string { return "fixed" })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first request got %d", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request got %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After tells the client nothing about when to come back")
	}
}

// Allow and Sweep run from request handlers and a background goroutine at the
// same time.
func TestRateLimiterIsSafeUnderConcurrency(t *testing.T) {
	rl := NewRateLimiter(1000, 1000)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				rl.Allow("client-" + strconv.Itoa(n%5))
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			rl.Sweep()
		}
	}()
	wg.Wait()
}
