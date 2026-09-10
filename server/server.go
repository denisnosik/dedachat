package server

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/denisnosik/dedachat/internal/database"
	_ "github.com/lib/pq"
)

type apiConfig struct {
	db     *database.Queries
	dbConn *sql.DB
	secret string
	hub    *Hub
}

func Run() {
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL must be set")
	}

	secret := os.Getenv("SECRET")
	if secret == "" {
		log.Fatal("SECRET must be set")
	}

	dbConn, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Error opening database: %s", err)
	}
	dbQueries := database.New(dbConn)

	hub := newHub()
	go hub.run()

	apiCfg := apiConfig{
		db:     dbQueries,
		dbConn: dbConn,
		secret: secret,
		hub:    hub,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", apiCfg.handlerHealthCheck)

	mux.HandleFunc("POST /api/register", apiCfg.handlerCreateUser)
	mux.HandleFunc("POST /api/login", apiCfg.handlerLoginUser)

	mux.HandleFunc("POST /api/chats", apiCfg.middlewareAuth(apiCfg.handlerChat))
	mux.HandleFunc("GET /api/chats/ws", apiCfg.handlerChatWS)
	mux.HandleFunc("POST /api/chats/{chat_id}/read", apiCfg.middlewareAuth(apiCfg.handlerMarkAsRead))

	mux.HandleFunc("GET /api/notifications", apiCfg.middlewareAuth(apiCfg.handlerNotifications))

	mux.HandleFunc("POST /api/friends", apiCfg.middlewareAuth(apiCfg.handlerFriends))
	mux.HandleFunc("GET /api/friends", apiCfg.middlewareAuth(apiCfg.handlerGetFriends))
	mux.HandleFunc("DELETE /api/friends", apiCfg.middlewareAuth(apiCfg.handlerDeleteFriend))

	mux.HandleFunc("GET /api/presence/ws", apiCfg.middlewareAuth(apiCfg.handlerPresenceWS))

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server error: %v", err)
	}
}
