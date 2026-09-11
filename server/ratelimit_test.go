package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// Unlike the rest of this package these are plain unit tests: the limiter is
// in-process state, so they need no running server.

func TestKeyedLimiterAllowsBurstThenThrottles(t *testing.T) {
	limiter := newKeyedLimiter(rate.Every(time.Minute), 2)

	for i := range 2 {
		ok, retryAfter := limiter.allow("client")
		require.Truef(t, ok, "request %d should be inside the burst", i)
		require.Zero(t, retryAfter)
	}

	ok, retryAfter := limiter.allow("client")
	require.False(t, ok)
	require.Positive(t, retryAfter, "a throttled caller must learn when to retry")

	// The rejected request may not eat a token, or a client that keeps trying
	// would never get back in.
	require.InDelta(t, time.Minute.Seconds(), retryAfter.Seconds(), 1)
	ok, _ = limiter.allow("client")
	require.False(t, ok)
}

func TestKeyedLimiterKeysAreIndependent(t *testing.T) {
	limiter := newKeyedLimiter(rate.Every(time.Minute), 1)

	ok, _ := limiter.allow("first")
	require.True(t, ok)

	ok, _ = limiter.allow("first")
	require.False(t, ok, "first client spent its only token")

	ok, _ = limiter.allow("second")
	require.True(t, ok, "second client must have its own bucket")
}

func TestKeyedLimiterDisabled(t *testing.T) {
	require.Nil(t, newKeyedLimiter(0, 10), "a zero rate disables the tier")
	require.Nil(t, newKeyedLimiter(rate.Every(time.Minute), 0), "a zero burst disables the tier")

	var disabled *keyedLimiter
	for range 100 {
		ok, retryAfter := disabled.allow("client")
		require.True(t, ok)
		require.Zero(t, retryAfter)
	}
}

func TestKeyedLimiterForgetsIdleBuckets(t *testing.T) {
	limiter := newKeyedLimiter(rate.Every(time.Second), 1)

	ok, _ := limiter.allow("client")
	require.True(t, ok)
	require.Len(t, limiter.buckets, 1)

	// Age both the bucket and the last sweep past the TTL instead of waiting.
	past := time.Now().Add(-2 * bucketTTL)
	limiter.buckets["client"].lastSeen = past
	limiter.lastSweep = past

	limiter.sweep(time.Now())
	require.Empty(t, limiter.buckets)
}

func TestMiddlewareRateLimitIPRespondsWithRetryAfter(t *testing.T) {
	cfg := apiConfig{
		limiters: rateLimiters{auth: newKeyedLimiter(rate.Every(time.Minute), 1)},
	}

	var handlerCalls int
	handler := cfg.middlewareRateLimitIP(cfg.limiters.auth, func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusOK)
	})

	first := httptest.NewRecorder()
	handler(first, httptest.NewRequest("POST", "/api/login", nil))
	require.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	handler(second, httptest.NewRequest("POST", "/api/login", nil))
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Equal(t, "60", second.Header().Get("Retry-After"))
	require.Equal(t, 1, handlerCalls, "a throttled request must not reach the handler")
}

func TestMiddlewareRateLimitIPSeparatesClients(t *testing.T) {
	cfg := apiConfig{
		limiters: rateLimiters{auth: newKeyedLimiter(rate.Every(time.Minute), 1)},
	}
	handler := cfg.middlewareRateLimitIP(cfg.limiters.auth, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	call := func(remoteAddr string) int {
		req := httptest.NewRequest("POST", "/api/login", nil)
		req.RemoteAddr = remoteAddr
		res := httptest.NewRecorder()
		handler(res, req)
		return res.Code
	}

	// Same host, different source ports: still one client.
	require.Equal(t, http.StatusOK, call("203.0.113.7:40001"))
	require.Equal(t, http.StatusTooManyRequests, call("203.0.113.7:40002"))
	require.Equal(t, http.StatusOK, call("203.0.113.8:40003"))
}

func TestClientIPIgnoresForwardedHeaderUnlessTrusted(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/login", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.2")

	untrusted := apiConfig{}
	require.Equal(t, "10.0.0.1", untrusted.clientIP(req),
		"a spoofable header must not hand out a fresh bucket per request")

	trusted := apiConfig{trustProxyHeaders: true}
	require.Equal(t, "198.51.100.9", trusted.clientIP(req),
		"behind a trusted proxy the leftmost entry is the real client")
}

func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	cfg := apiConfig{trustProxyHeaders: true}

	req := httptest.NewRequest("POST", "/api/login", nil)
	req.RemoteAddr = "unix"
	req.Header.Set("X-Forwarded-For", "   ")

	require.Equal(t, "unix", cfg.clientIP(req))
}

func TestEnvOverrides(t *testing.T) {
	require.Equal(t, 42.0, envFloat("RATE_LIMIT_TEST_FLOAT", 42))
	require.Equal(t, 7, envInt("RATE_LIMIT_TEST_INT", 7))
	require.False(t, envBool("RATE_LIMIT_TEST_BOOL", false))

	t.Setenv("RATE_LIMIT_TEST_FLOAT", "0.5")
	t.Setenv("RATE_LIMIT_TEST_INT", "0")
	t.Setenv("RATE_LIMIT_TEST_BOOL", "true")

	require.Equal(t, 0.5, envFloat("RATE_LIMIT_TEST_FLOAT", 42))
	require.Equal(t, 0, envInt("RATE_LIMIT_TEST_INT", 7))
	require.True(t, envBool("RATE_LIMIT_TEST_BOOL", false))
}
