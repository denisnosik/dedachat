package server

import (
	"context"
	"uuid"
)

type Hub struct {
	clients            map[*Client]bool
	presenceClients    map[*presenceClient]bool
	online             map[uuid.UUID]int
	broadcast          chan *Message
	register           chan *Client
	unregister         chan *Client
	registerPresence   chan *presenceClient
	unregisterPresence chan *presenceClient
	onlineReq          chan onlineRequest
	setOnline          chan uuid.UUID
	setOffline         chan uuid.UUID
	shutdown           chan struct{}
	done               chan struct{}
}

type onlineRequest struct {
	userID uuid.UUID
	res    chan bool
}

func newHub() *Hub {
	return &Hub{
		clients:            make(map[*Client]bool),
		presenceClients:    make(map[*presenceClient]bool),
		online:             make(map[uuid.UUID]int),
		broadcast:          make(chan *Message),
		register:           make(chan *Client),
		unregister:         make(chan *Client),
		registerPresence:   make(chan *presenceClient),
		unregisterPresence: make(chan *presenceClient),
		onlineReq:          make(chan onlineRequest),
		setOnline:          make(chan uuid.UUID),
		setOffline:         make(chan uuid.UUID),
		shutdown:           make(chan struct{}),
		done:               make(chan struct{}),
	}
}

func (h *Hub) run() {
	defer close(h.done)

	// Local copy so it can be nilled out after the shutdown signal: a closed
	// channel is always ready, and selecting on it again would spin.
	shutdown := h.shutdown
	closing := false

	for {
		// Shutdown hangs up every socket but does not wait here — the read
		// loops still have to report in through unregister, and run() is the
		// only goroutine allowed to touch these maps, so it stays alive until
		// the last one has.
		if closing && len(h.clients) == 0 && len(h.presenceClients) == 0 {
			return
		}

		select {
		case client := <-h.register:
			if closing {
				// Slipped in behind the shutdown, so it never got a close
				// frame. Hang it up instead of tracking it.
				closeConn(client.conn, websocketCloseShutdown, "server shutting down")
				continue
			}
			h.clients[client] = true

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}

		case client := <-h.registerPresence:
			if closing {
				closeConn(client.conn, websocketCloseShutdown, "server shutting down")
				continue
			}
			h.presenceClients[client] = true

		case client := <-h.unregisterPresence:
			delete(h.presenceClients, client)

		case msg := <-h.broadcast:
			for client := range h.clients {
				if client.chatID != msg.chatID {
					continue
				}

				if client.userID == msg.senderID {
					continue
				}

				select {
				case client.send <- msg.payload:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}

		case req := <-h.onlineReq:
			req.res <- h.online[req.userID] > 0

		case userID := <-h.setOnline:
			h.online[userID]++

		case userID := <-h.setOffline:
			h.online[userID]--
			if h.online[userID] < 0 {
				h.online[userID] = 0
			}

		case <-shutdown:
			closing = true
			shutdown = nil
			h.closeAll()
		}
	}
}

func (h *Hub) IsOnline(userID uuid.UUID) bool {
	res := make(chan bool)
	h.onlineReq <- onlineRequest{
		userID: userID,
		res:    res,
	}
	return <-res
}

func (h *Hub) SetOnline(userID uuid.UUID) {
	h.setOnline <- userID
}

func (h *Hub) SetOffline(userID uuid.UUID) {
	h.setOffline <- userID
}

// Shutdown hangs up every live socket and waits for the hub goroutine to
// finish. http.Server.Shutdown can't do this: a hijacked WebSocket is invisible
// to it, so without this the process would exit while connections were still
// mid-write, and clients would see an abnormal close instead of a close frame.
//
// It must be called at most once, from Run.
func (h *Hub) Shutdown(ctx context.Context) error {
	close(h.shutdown)

	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closeAll sends a close frame on every connection and closes it. Closing the
// socket (rather than waiting for the peer to answer the frame) is what makes
// shutdown bounded: each read loop returns at once and unregisters, instead of
// hanging around until pongWait for a client that is not listening.
func (h *Hub) closeAll() {
	for client := range h.clients {
		closeConn(client.conn, websocketCloseShutdown, "server shutting down")
	}
	for client := range h.presenceClients {
		closeConn(client.conn, websocketCloseShutdown, "server shutting down")
	}
}
