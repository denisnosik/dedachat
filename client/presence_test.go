package client

import (
	"fmt"
	"net/http"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
)

// These tests talk to a real server, the same way the server package's tests
// do: start the stack with docker-compose.ci.yml first.
const testServer = "http://localhost:8080"

func testToken(t *testing.T) string {
	t.Helper()

	baseURL = testServer
	c := Client{httpClient: http.Client{Timeout: 5 * time.Second}}

	nickname := fmt.Sprintf("test_presence_%d", uuid.New())
	_, err := c.register(nickname, "000000")
	require.NoError(t, err)

	res, err := c.login(nickname, "000000")
	require.NoError(t, err)
	require.NotEmpty(t, res.Token)

	return res.Token
}

// requirePrompt fails if fn has not returned within a second. Stop waits for
// the close frame to go out, so a mistake there hangs the whole client on quit
// rather than showing up as a wrong value somewhere.
func requirePrompt(t *testing.T, what string, fn func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, what+" did not return within 1s")
	}
}

func TestPresenceStartStop(t *testing.T) {
	token := testToken(t)

	p := newPresence()
	require.NoError(t, p.Start(token))

	requirePrompt(t, "Stop", p.Stop)
}

func TestPresenceStopWithoutStart(t *testing.T) {
	p := newPresence()

	requirePrompt(t, "Stop", p.Stop)
}

func TestPresenceStopTwice(t *testing.T) {
	token := testToken(t)

	p := newPresence()
	require.NoError(t, p.Start(token))

	requirePrompt(t, "first Stop", p.Stop)
	requirePrompt(t, "second Stop", p.Stop)
}

func TestPresenceRestartReplacesSession(t *testing.T) {
	first := testToken(t)
	second := testToken(t)

	p := newPresence()
	require.NoError(t, p.Start(first))
	// Logging in again must not leave the previous session's loop running.
	require.NoError(t, p.Start(second))

	requirePrompt(t, "Stop", p.Stop)
}

func TestPresenceStartFailsWhenServerIsUnreachable(t *testing.T) {
	baseURL = "http://127.0.0.1:1"
	defer func() { baseURL = testServer }()

	p := newPresence()
	require.Error(t, p.Start("irrelevant"))

	// A failed Start must leave nothing behind to stop.
	requirePrompt(t, "Stop", p.Stop)
}

func TestPresenceStartFailsWithBadToken(t *testing.T) {
	baseURL = testServer

	p := newPresence()
	require.Error(t, p.Start("not-a-jwt"))

	requirePrompt(t, "Stop", p.Stop)
}
