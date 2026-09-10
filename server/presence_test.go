package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func dialPresenceWS(t *testing.T, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/presence/ws"

	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}

	return websocket.DefaultDialer.Dial(wsURL, header)
}

// isOnline asks user how the server currently reports friend's status.
func isOnline(t *testing.T, user testUser, friendNickname string) bool {
	t.Helper()

	req, err := http.NewRequest("GET", baseURL+"/api/friends", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+user.token)

	res, err := testClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)

	var friends []struct {
		Nickname string `json:"nickname"`
		Online   bool   `json:"online"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&friends))

	for _, f := range friends {
		if f.Nickname == friendNickname {
			return f.Online
		}
	}

	t.Fatalf("%s is not in %s's friends list", friendNickname, user.nickname)
	return false
}

// requireStatus waits for the reported status to settle: the hub applies a
// disconnect asynchronously, once the read loop notices the closed socket.
func requireStatus(t *testing.T, user testUser, friendNickname string, want bool) {
	t.Helper()

	require.Eventually(t, func() bool {
		return isOnline(t, user, friendNickname) == want
	}, 5*time.Second, 50*time.Millisecond, "expected %s to be online=%v", friendNickname, want)
}

func TestPresence(t *testing.T) {
	withLeakCheck(t)

	t.Run("offline without a presence connection", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		makeFriends(t, user, friend)

		require.False(t, isOnline(t, user, friend.nickname))
	})

	t.Run("online while the socket is open, offline once it closes", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		makeFriends(t, user, friend)

		conn, res, err := dialPresenceWS(t, friend.token)
		require.NoError(t, err)
		defer res.Body.Close()

		requireStatus(t, user, friend.nickname, true)

		require.NoError(t, conn.Close())

		requireStatus(t, user, friend.nickname, false)
	})

	t.Run("stays online until the last connection closes", func(t *testing.T) {
		user := createAndLoginUser(t)
		friend := createAndLoginUser(t)
		makeFriends(t, user, friend)

		first, res1, err := dialPresenceWS(t, friend.token)
		require.NoError(t, err)
		defer res1.Body.Close()

		second, res2, err := dialPresenceWS(t, friend.token)
		require.NoError(t, err)
		defer res2.Body.Close()

		requireStatus(t, user, friend.nickname, true)

		require.NoError(t, first.Close())

		// One session ended, the other is still up.
		require.True(t, isOnline(t, user, friend.nickname))

		require.NoError(t, second.Close())

		requireStatus(t, user, friend.nickname, false)
	})

	t.Run("unauthenticated", func(t *testing.T) {
		conn, res, err := dialPresenceWS(t, "")
		if conn != nil {
			defer conn.Close()
		}
		require.ErrorIs(t, err, websocket.ErrBadHandshake)
		require.NotNil(t, res)
		defer res.Body.Close()
		require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	})

	t.Run("invalid token", func(t *testing.T) {
		conn, res, err := dialPresenceWS(t, "not-a-jwt")
		if conn != nil {
			defer conn.Close()
		}
		require.ErrorIs(t, err, websocket.ErrBadHandshake)
		require.NotNil(t, res)
		defer res.Body.Close()
		require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	})
}
