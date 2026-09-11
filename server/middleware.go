package server

import (
	"context"
	"net/http"

	"github.com/denisnosik/dedachat/internal/auth"
)

type contextKey string

const contextKeyUserID contextKey = "currentUserID"

func (cfg *apiConfig) middlewareAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := auth.GetBearerToken(r.Header)
		if err != nil {
			respondWithError(w, http.StatusUnauthorized, "Couldn't get JWT", err)
			return
		}

		currentUserID, err := auth.ValidateJWT(token, cfg.secret)
		if err != nil {
			respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
			return
		}

		// Per-user rate limiting lives here rather than on each route, so that
		// adding a route can't accidentally leave it unlimited. The key is the
		// user, not the IP: sessions of the same user share a budget, and
		// different users behind one NAT don't.
		if ok, retryAfter := cfg.limiters.api.allow(currentUserID.String()); !ok {
			respondTooManyRequests(w, retryAfter)
			return
		}

		ctx := context.WithValue(r.Context(), contextKeyUserID, currentUserID)
		next(w, r.WithContext(ctx))
	}
}
