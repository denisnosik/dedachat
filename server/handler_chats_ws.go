package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
	"uuid"

	"github.com/denisnosik/dedachat/internal/auth"
	"github.com/denisnosik/dedachat/internal/database"
	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

type Client struct {
	conn     *websocket.Conn
	hub      *Hub
	userID   uuid.UUID
	nickname string
	chatID   uuid.UUID
	send     chan []byte
	// limiter throttles inbound messages. It is per connection, so it needs no
	// locking: only readFromClient touches it.
	limiter *rate.Limiter
}

type Message struct {
	chatID   uuid.UUID
	senderID uuid.UUID
	payload  []byte
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512

	// websocketCloseShutdown tells the peer the server is going away on
	// purpose, so a reconnecting client knows it wasn't a network fault.
	websocketCloseShutdown = websocket.CloseServiceRestart
)

// closeConn sends a close frame and then closes the socket. WriteControl and
// Close are the only gorilla methods safe to call from a goroutine that doesn't
// own the connection, which is what lets the hub hang up a client on shutdown.
func closeConn(conn *websocket.Conn, code int, reason string) {
	msg := websocket.FormatCloseMessage(code, reason)

	err := conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(writeWait))
	if err != nil && !errors.Is(err, websocket.ErrCloseSent) && !errors.Is(err, net.ErrClosed) {
		log.Printf("error writing close frame: %v", err)
	}

	closeSocket(conn)
}

// closeSocket closes a connection, tolerating one that is already closed: on
// shutdown the hub hangs up first, and then the read loop that noticed runs its
// own deferred close.
func closeSocket(conn *websocket.Conn) {
	logWSError("closing websocket connection", conn.Close())
}

// logWSError logs a socket error unless it is one of the two expected ways a
// connection ends: the hub already hung it up, or a close frame has already
// gone out. Both are routine during shutdown and would otherwise print one
// scary line per connected client.
func logWSError(action string, err error) {
	if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, websocket.ErrCloseSent) {
		return
	}
	log.Printf("error %s: %v", action, err)
}

type wsMessage struct {
	Nickname  string    `json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
	Content   string    `json:"content"`
}

func (cfg *apiConfig) handlerChatWS(w http.ResponseWriter, r *http.Request) {
	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		respondWithError(w, http.StatusBadRequest, "chat_id required", nil)
		return
	}

	parsedChatID, err := uuid.Parse(chatID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "invalid chat_id", err)
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		respondWithError(w, http.StatusBadRequest, "token required", nil)
		return
	}

	currentUserID, err := auth.ValidateJWT(token, cfg.secret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Everything that can fail must happen before the upgrade: once the
	// connection is hijacked, respondWithError can no longer reach the client.
	_, err = cfg.db.GetChatMember(r.Context(), database.GetChatMemberParams{
		ChatID: parsedChatID,
		UserID: currentUserID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondWithError(w, http.StatusForbidden, "not a member of this chat", nil)
			return
		}
		respondWithError(w, http.StatusInternalServerError, "Couldn't get chat member from db", err)
		return
	}

	user, err := cfg.db.GetUserByID(r.Context(), currentUserID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get user from db", err)
		return
	}

	messages, err := cfg.db.GetMessagesByChat(r.Context(), database.GetMessagesByChatParams{
		ChatID: parsedChatID,
		Limit:  50, // for chat history, loads last 50 msgs
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get messages from db", err)
		return
	}

	err = cfg.db.MarkMessagesAsRead(r.Context(), database.MarkMessagesAsReadParams{
		ChatID:   parsedChatID,
		SenderID: currentUserID,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't mark messages as read from db", err)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response to the client.
		log.Printf("error upgrading connection: %v", err)
		return
	}

	client := &Client{
		conn:     conn,
		hub:      cfg.hub,
		userID:   currentUserID,
		nickname: user.Nickname,
		chatID:   parsedChatID,
		send:     make(chan []byte, 256),
		limiter:  rate.NewLimiter(wsMessagesPerSec, wsMessageBurst),
	}

	// History goes into the buffer before the hub is told about the client:
	// after register the send channel belongs to the hub, which may close it
	// (on a broadcast backlog, or on shutdown) while this handler still runs.
	// The buffer is far larger than the 50-message history, so this can't block.
	for _, msg := range messages {
		payload, err := json.Marshal(wsMessage{
			Nickname:  msg.Nickname,
			CreatedAt: msg.CreatedAt,
			Content:   msg.Content,
		})
		if err != nil {
			log.Printf("Couldn't marshal history message: %v", err)
			continue
		}
		client.send <- payload
	}

	cfg.hub.register <- client

	go client.writeToClient()
	go client.readFromClient(cfg) /* #nosec G118 -- the connection outlives the request, so r.Context() would cancel every write as soon as this handler returns */
}

func (c *Client) writeToClient() {
	ticker := time.NewTicker(pingPeriod)

	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-c.send:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				logWSError("setting write deadline", err)
				return
			}
			if !ok {
				// The hub closed the channel. On shutdown it has already sent
				// a close frame and closed the socket, so this one is best
				// effort; when the channel was dropped for a full send buffer
				// instead, it is the client's only goodbye.
				logWSError("writing close message", c.conn.WriteMessage(websocket.CloseMessage, []byte{}))
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				logWSError("getting next writer", err)
				return
			}
			_, err = w.Write(msg)
			if err != nil {
				logWSError("writing message", err)
				return
			}

			if err := w.Close(); err != nil {
				logWSError("closing writer", err)
				return
			}
		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				logWSError("setting write deadline", err)
				return
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				logWSError("writing ping", err)
				return
			}
		}
	}
}

func (c *Client) readFromClient(cfg *apiConfig) {
	defer func() {
		c.hub.unregister <- c
		closeSocket(c.conn)
	}()

	c.conn.SetReadLimit(maxMessageSize)

	if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Printf("error setting read deadline: %v", err)
		return
	}

	c.conn.SetPongHandler(func(string) error {
		if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			log.Printf("error setting read deadline in pong handler: %v", err)
		}
		return nil
	})

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure,
			) {
				log.Printf("error: %v", err)
			}
			break
		}

		// Nobody types faster than wsMessagesPerSec, so this only trips on a
		// buggy or spamming client. Dropping the message silently would be
		// worse than hanging up: the TUI echoes what it sends locally, so the
		// sender would see a message the other side never got.
		if !c.limiter.Allow() {
			log.Printf("message rate limit exceeded by user %s, closing socket", c.userID)
			closeConn(c.conn, websocket.ClosePolicyViolation, "message rate limit exceeded")
			break
		}

		dbMsg, err := cfg.db.CreateMessage(context.Background(), database.CreateMessageParams{
			ChatID:   c.chatID,
			SenderID: c.userID,
			Content:  string(msg),
		})
		if err != nil {
			log.Printf("db error: %v", err)
			continue
		}

		payload, err := json.Marshal(wsMessage{
			Nickname:  c.nickname,
			CreatedAt: dbMsg.CreatedAt,
			Content:   string(msg),
		})
		if err != nil {
			log.Printf("Couldn't marshal: %v", err)
			continue
		}

		c.hub.broadcast <- &Message{
			chatID:   c.chatID,
			senderID: c.userID,
			payload:  payload,
		}
	}
}
