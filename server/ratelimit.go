package server

import (
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limits come in tiers, each an independent token bucket per key:
//
//   - auth — /api/register and /api/login, keyed by client IP. No user exists
//     yet at that point, and login runs argon2id, so this tier is the strict
//     one: it is what stands between the server and a password guesser.
//   - api — everything behind middlewareAuth, keyed by user ID, so one noisy
//     session can't spend another user's budget.
//   - ws — the chat socket upgrade, keyed by client IP because that route is
//     deliberately not behind middlewareAuth. It reuses the api numbers.
//
// Defaults are generous for a human driving the TUI and stingy for a script
// hammering the credential endpoints. Every value can be overridden with the
// matching env var; 0 disables that tier. The CI stack raises them because the
// whole test suite registers dozens of users from one IP in a few seconds.
const (
	defaultAuthPerMin = 20
	defaultAuthBurst  = 10
	defaultAPIPerSec  = 20
	defaultAPIBurst   = 40

	// Chat messages are limited per connection rather than per key: the socket
	// is already authenticated and scoped to one chat, so the limiter can just
	// live on the Client.
	wsMessagesPerSec = 5
	wsMessageBurst   = 10

	// A bucket idle for this long is indistinguishable from a fresh one — it
	// has refilled to full burst — so forgetting it is free. Keep it well above
	// burst/rate for every tier or the eviction would hand out extra tokens.
	bucketTTL = 10 * time.Minute
)

type rateLimiters struct {
	auth *keyedLimiter
	api  *keyedLimiter
	ws   *keyedLimiter
}

func newRateLimiters() rateLimiters {
	authPerMin := envFloat("RATE_LIMIT_AUTH_PER_MIN", defaultAuthPerMin)
	authBurst := envInt("RATE_LIMIT_AUTH_BURST", defaultAuthBurst)
	apiPerSec := envFloat("RATE_LIMIT_API_PER_SEC", defaultAPIPerSec)
	apiBurst := envInt("RATE_LIMIT_API_BURST", defaultAPIBurst)

	return rateLimiters{
		auth: newKeyedLimiter(rate.Limit(authPerMin/60), authBurst),
		api:  newKeyedLimiter(rate.Limit(apiPerSec), apiBurst),
		ws:   newKeyedLimiter(rate.Limit(apiPerSec), apiBurst),
	}
}

// keyedLimiter hands out one token bucket per key and forgets keys that stop
// showing up, so a stream of one-shot IPs can't grow the map forever.
//
// A nil *keyedLimiter is a disabled tier and allows everything, which is how
// setting a limit to 0 in the environment turns it off.
type keyedLimiter struct {
	limit rate.Limit
	burst int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newKeyedLimiter(limit rate.Limit, burst int) *keyedLimiter {
	if limit <= 0 || burst <= 0 {
		return nil
	}

	return &keyedLimiter{
		limit:     limit,
		burst:     burst,
		buckets:   make(map[string]*bucket),
		lastSweep: time.Now(),
	}
}

// allow reports whether key may spend a token now and, when it may not, how
// long until it can — that answer becomes the Retry-After header.
func (k *keyedLimiter) allow(key string) (bool, time.Duration) {
	if k == nil {
		return true, 0
	}

	now := time.Now()

	k.mu.Lock()
	defer k.mu.Unlock()

	k.sweep(now)

	b, ok := k.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(k.limit, k.burst)}
		k.buckets[key] = b
	}
	b.lastSeen = now

	// Reserve rather than Allow, because only a reservation reports the wait;
	// cancelling it puts the token back for whoever comes next.
	res := b.limiter.ReserveN(now, 1)
	if !res.OK() {
		return false, 0
	}
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return false, delay
	}

	return true, 0
}

// sweep drops buckets nobody has touched for bucketTTL. It runs on the request
// path instead of in a janitor goroutine: there is nothing to shut down, and
// the work is bounded by how rarely it happens.
func (k *keyedLimiter) sweep(now time.Time) {
	if now.Sub(k.lastSweep) < bucketTTL {
		return
	}
	k.lastSweep = now

	for key, b := range k.buckets {
		if now.Sub(b.lastSeen) >= bucketTTL {
			delete(k.buckets, key)
		}
	}
}

// middlewareRateLimitIP rejects a request when the bucket for its client IP is
// empty. Authenticated routes don't need it: middlewareAuth applies the
// per-user tier itself, so a new route can't forget to be limited.
func (cfg *apiConfig) middlewareRateLimitIP(limiter *keyedLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, retryAfter := limiter.allow(cfg.clientIP(r)); !ok {
			respondTooManyRequests(w, retryAfter)
			return
		}
		next(w, r)
	}
}

// clientIP is the rate-limit key for requests that have no user attached yet.
//
// X-Forwarded-For is only trusted when TRUST_PROXY_HEADERS is set, because a
// client can put anything in it: trusting it unconditionally would mean a
// spoofed header buys a fresh bucket per request. The flip side is that behind
// a proxy (Railway, nginx, a load balancer) it *must* be set, or every request
// arrives from the proxy and the whole world shares one bucket.
func (cfg *apiConfig) clientIP(r *http.Request) string {
	if cfg.trustProxyHeaders {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// Leftmost entry is the original client; the rest are proxy hops.
			if client := strings.TrimSpace(strings.Split(forwarded, ",")[0]); client != "" {
				return client
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port to strip (shouldn't happen for a served request) — key on
		// whatever we were given rather than lumping callers together.
		return r.RemoteAddr
	}
	return host
}

// respondTooManyRequests answers a throttled request. It deliberately logs
// nothing: a flood would otherwise turn into a log flood.
func respondTooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	if retryAfter > 0 {
		seconds := int(math.Ceil(retryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	respondWithError(w, http.StatusTooManyRequests, "Too many requests", nil)
}

// envFloat and envInt read an optional limit from the environment. An
// unparsable or negative value is fatal rather than ignored, so a typo can't
// silently remove a limit.
func envFloat(name string, def float64) float64 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		// The value itself stays out of the log: gosec flags operator input in
		// log lines, and whoever set the variable can read it back.
		log.Fatalf("%s must be a non-negative number", name)
	}
	return value
}

func envInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		log.Fatalf("%s must be a non-negative integer", name)
	}
	return value
}

func envBool(name string, def bool) bool {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Fatalf("%s must be a boolean (true or false)", name)
	}
	return value
}
