package client

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	presenceRetryDelay = 3 * time.Second
	presenceDialWait   = 5 * time.Second
	presenceWriteWait  = 10 * time.Second
	// Must exceed the server's ping period, otherwise a healthy connection
	// times out between pings.
	presenceReadWait  = 70 * time.Second
	presenceReadLimit = 512
)

// wsBaseURL turns the HTTP base address into its WebSocket equivalent.
func wsBaseURL() string {
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) // for localhost
	return strings.Replace(wsURL, "https://", "wss://", 1)   // for server
}

// presence holds a socket open for the whole session. The server counts the
// user as online for exactly as long as it stays up, so this reconnects by
// itself after a network drop and closes cleanly on quit.
//
// Everything mutable lives behind mu because Start is called from a Bubble Tea
// command goroutine while Stop is called from the signal handler and from Run.
type presence struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped chan struct{}
}

func newPresence() *presence {
	return &presence{}
}

// Start dials once synchronously so that a failure surfaces as a login error,
// then hands the connection to a background loop that keeps it alive.
func (p *presence) Start(token string) error {
	// Logging in again while already connected replaces the old session.
	p.Stop()

	ctx, cancel := context.WithCancel(context.Background())

	conn, err := dialPresence(ctx, token)
	if err != nil {
		cancel()
		return err
	}

	stopped := make(chan struct{})

	p.mu.Lock()
	p.cancel = cancel
	p.stopped = stopped
	p.mu.Unlock()

	go func() {
		defer close(stopped)
		p.run(ctx, token, conn)
	}()

	return nil
}

// Stop closes the connection and waits for the close frame to go out, so the
// user shows up as offline immediately instead of after the server's timeout.
func (p *presence) Stop() {
	p.mu.Lock()
	cancel, stopped := p.cancel, p.stopped
	p.cancel, p.stopped = nil, nil
	p.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	<-stopped
}

func (p *presence) run(ctx context.Context, token string, conn *websocket.Conn) {
	for {
		hold(ctx, conn)

		if ctx.Err() != nil {
			return
		}

		// The server drops us from the online set the moment the socket dies,
		// so keep retrying until the session actually ends.
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(presenceRetryDelay):
			}

			var err error
			conn, err = dialPresence(ctx, token)
			if err == nil {
				break
			}
		}
	}
}

func dialPresence(ctx context.Context, token string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: presenceDialWait}

	conn, res, err := dialer.DialContext(ctx, wsBaseURL()+"/api/presence/ws", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// hold blocks until the connection dies or ctx is cancelled. The server never
// sends application messages on this socket; reading is what answers pings and
// notices that the connection is gone.
func hold(ctx context.Context, conn *websocket.Conn) {
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			// A close frame lets the server drop us from the online set right
			// away rather than waiting for the read deadline to expire.
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(presenceWriteWait),
			)
		case <-done:
			// hold returned on its own; the connection is already broken.
		}

		// Unblocks the ReadMessage loop below when the cancellation came first.
		_ = conn.Close()
	}()

	conn.SetReadLimit(presenceReadLimit)

	if err := conn.SetReadDeadline(time.Now().Add(presenceReadWait)); err != nil {
		return
	}

	conn.SetPingHandler(func(appData string) error {
		if err := conn.SetReadDeadline(time.Now().Add(presenceReadWait)); err != nil {
			return err
		}
		return conn.WriteControl(
			websocket.PongMessage,
			[]byte(appData),
			time.Now().Add(presenceWriteWait),
		)
	})

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}
