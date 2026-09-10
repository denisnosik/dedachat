package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"uuid"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func createChat(t *testing.T, targetNickname, userToken string) uuid.UUID {
	t.Helper()

	res := createChatRequest(t, targetNickname, userToken)
	defer res.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, res.StatusCode)

	var chat Chat
	require.NoError(t, json.NewDecoder(res.Body).Decode(&chat))
	require.NotEqual(t, uuid.UUID{}, chat.ID)

	return chat.ID
}

func dialChatWS(t *testing.T, chatID, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http")
	wsURL += "/api/chats/ws?chat_id=" + url.QueryEscape(chatID) + "&token=" + url.QueryEscape(token)

	return websocket.DefaultDialer.Dial(wsURL, nil)
}

// dialChatWSExpectError asserts the handshake was rejected with wantStatus.
func dialChatWSExpectError(t *testing.T, chatID, token string, wantStatus int) {
	t.Helper()

	conn, res, err := dialChatWS(t, chatID, token)
	if conn != nil {
		defer conn.Close()
	}
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, res)
	defer res.Body.Close()
	require.Equal(t, wantStatus, res.StatusCode)
}

func TestChatWS(t *testing.T) {
	withLeakCheck(t)

	t.Run("member connects", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		makeFriends(t, user, friend)
		chatID := createChat(t, friend.nickname, user.token)

		conn, res, err := dialChatWS(t, chatID.String(), user.token)
		require.NoError(t, err)
		defer conn.Close()
		defer res.Body.Close()
	})

	t.Run("non-member is rejected", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		stranger := createAndLoginUser(t)
		makeFriends(t, user, friend)
		chatID := createChat(t, friend.nickname, user.token)

		dialChatWSExpectError(t, chatID.String(), stranger.token, http.StatusForbidden)
	})

	t.Run("nonexistent chat is rejected", func(t *testing.T) {
		user := createAndLoginUser(t)

		dialChatWSExpectError(t, uuid.New().String(), user.token, http.StatusForbidden)
	})

	t.Run("invalid token", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		makeFriends(t, user, friend)
		chatID := createChat(t, friend.nickname, user.token)

		dialChatWSExpectError(t, chatID.String(), "not-a-jwt", http.StatusUnauthorized)
	})

	t.Run("missing token", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		makeFriends(t, user, friend)
		chatID := createChat(t, friend.nickname, user.token)

		dialChatWSExpectError(t, chatID.String(), "", http.StatusBadRequest)
	})

	t.Run("invalid chat_id", func(t *testing.T) {
		user := createAndLoginUser(t)

		dialChatWSExpectError(t, "not-a-uuid", user.token, http.StatusBadRequest)
	})

	t.Run("missing chat_id", func(t *testing.T) {
		user := createAndLoginUser(t)

		dialChatWSExpectError(t, "", user.token, http.StatusBadRequest)
	})
}
