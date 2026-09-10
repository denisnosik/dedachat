package server

import (
	"log"
	"net/http"
	"time"
	"uuid"

	"github.com/gorilla/websocket"
)

// presenceClient is a connection whose only job is to exist: the user counts as
// online for exactly as long as the socket is alive. Unlike the chat socket it
// carries no application messages, so a client keeps it open for the whole
// session rather than only while a chat is open.
//
// Liveness comes from ping/pong: a client that crashes, loses the network or
// has its process killed stops answering and falls out of the online set within
// pongWait, instead of staying online forever the way an explicit
// "I am leaving" HTTP call would.
type presenceClient struct {
	conn   *websocket.Conn
	hub    *Hub
	userID uuid.UUID
	done   chan struct{}
}

func (cfg *apiConfig) handlerPresenceWS(w http.ResponseWriter, r *http.Request) {
	currentUserID := r.Context().Value(contextKeyUserID).(uuid.UUID)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response to the client.
		log.Printf("error upgrading presence connection: %v", err)
		return
	}

	client := &presenceClient{
		conn:   conn,
		hub:    cfg.hub,
		userID: currentUserID,
		done:   make(chan struct{}),
	}

	cfg.hub.SetOnline(currentUserID)

	go client.writePings()
	go client.read()
}

// read drains the socket until it dies. No message is ever expected on it;
// reading is what processes pongs and notices that the peer is gone.
func (c *presenceClient) read() {
	defer func() {
		close(c.done)
		c.hub.SetOffline(c.userID)
		if err := c.conn.Close(); err != nil {
			log.Printf("error closing presence connection: %v", err)
		}
	}()

	c.conn.SetReadLimit(maxMessageSize)

	if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Printf("error setting presence read deadline: %v", err)
		return
	}

	c.conn.SetPongHandler(func(string) error {
		if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			log.Printf("error setting presence read deadline in pong handler: %v", err)
		}
		return nil
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure,
			) {
				log.Printf("presence error: %v", err)
			}
			return
		}
	}
}

func (c *presenceClient) writePings() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return

		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				log.Printf("error setting presence write deadline: %v", err)
				return
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("error writing presence ping: %v", err)
				return
			}
		}
	}
}
