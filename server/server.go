package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/denisnosik/dedachat/internal/database"
	_ "github.com/lib/pq"
)

// shutdownTimeout bounds the whole drain — in-flight HTTP requests first, then
// hanging up the WebSockets. It sits under Docker's default ten second stop
// grace period so that a normal `docker compose down` ends with the server
// exiting on its own rather than being killed mid-drain.
const shutdownTimeout = 8 * time.Second

type apiConfig struct {
	db                *database.Queries
	dbConn            *sql.DB
	secret            string
	hub               *Hub
	limiters          rateLimiters
	trustProxyHeaders bool
	// debugClientIP is TEMPORARY: see logClientIPKey in ratelimit.go.
	debugClientIP bool
}

func Run() {
	// Signal handling is armed before anything is opened, so a Ctrl-C during
	// startup is still a clean shutdown rather than a half-initialised exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		db:                dbQueries,
		dbConn:            dbConn,
		secret:            secret,
		hub:               hub,
		limiters:          newRateLimiters(),
		trustProxyHeaders: envBool("TRUST_PROXY_HEADERS", false),
		debugClientIP:     envBool("DEBUG_CLIENT_IP", false), // TEMPORARY
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", apiCfg.handlerHealthCheck)

	mux.HandleFunc("POST /api/register", apiCfg.middlewareRateLimitIP(apiCfg.limiters.auth, apiCfg.handlerCreateUser))
	mux.HandleFunc("POST /api/login", apiCfg.middlewareRateLimitIP(apiCfg.limiters.auth, apiCfg.handlerLoginUser))

	mux.HandleFunc("POST /api/chats", apiCfg.middlewareAuth(apiCfg.handlerChat))

	mux.HandleFunc("GET /api/chats/ws", apiCfg.middlewareRateLimitIP(apiCfg.limiters.ws, apiCfg.handlerChatWS))
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

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()
	log.Printf("Listening on %s", server.Addr)

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server error: %v", err)
		}
	case <-ctx.Done():
		log.Println("Shutdown signal received, draining connections")
	}

	// Signals go back to their default behaviour: a second Ctrl-C now kills the
	// process instead of being swallowed by a drain that is taking too long.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Order matters. Shutdown stops the listener and waits for in-flight HTTP
	// requests, but returns immediately for hijacked connections, so the
	// WebSockets are still live afterwards and the hub hangs them up. Only then
	// is nothing left that could still query the database.
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error shutting down http server: %v", err)
	}
	if err := hub.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error shutting down hub: %v", err)
	}
	if err := dbConn.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	}

	log.Println("Shutdown complete")
}
