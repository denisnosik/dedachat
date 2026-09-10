package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
	"uuid"

	"github.com/denisnosik/dedachat/internal/auth"
	"github.com/denisnosik/dedachat/internal/database"
	"github.com/gorilla/websocket"
)

type Client struct {
	conn     *websocket.Conn
	hub      *Hub
	userID   uuid.UUID
	nickname string
	chatID   uuid.UUID
	send     chan []byte
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
)

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
	}

	cfg.hub.register <- client

	if len(messages) > 0 {
		for _, msg := range messages {
			payload, _ := json.Marshal(wsMessage{
				Nickname:  msg.Nickname,
				CreatedAt: msg.CreatedAt,
				Content:   msg.Content,
			})
			client.send <- payload
		}
	}

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
				log.Printf("error setting write deadline: %v", err)
				return
			}
			if !ok {
				if err := c.conn.WriteMessage(websocket.CloseMessage, []byte{}); err != nil {
					log.Printf("error write message: %v", err)
					return
				}
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Printf("error next writer: %v", err)
				return
			}
			_, err = w.Write(msg)
			if err != nil {
				log.Printf("error writing message: %v", err)
				return
			}

			if err := w.Close(); err != nil {
				log.Printf("error closing writer: %v", err)
				return
			}
		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				log.Printf("error setting write deadline: %v", err)
				return
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("error write message: %v", err)
				return
			}
		}
	}
}

func (c *Client) readFromClient(cfg *apiConfig) {
	defer func() {
		c.hub.unregister <- c
		if err := c.conn.Close(); err != nil {
			log.Printf("error closing websocket connection: %v", err)
		}
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
