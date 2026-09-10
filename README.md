
![ci badge](https://github.com/denisnosik/dedachat/actions/workflows/ci.yml/badge.svg)

# DEDA Chat
An open-source real-time messaging system built in Go.

DEDA Chat is a self-hostable, open-source messaging system designed to be simple, extensible, and production-ready out of the box. It provides everything you need to run your own real-time chat platform: authentication, friend management, WebSocket-based messaging, and a terminal client, all in one place.

<img width="800" height="450" alt="dedachat_gif_main-ezgif com-video-to-gif-converter" src="https://github.com/user-attachments/assets/cd103509-3ced-4612-a816-041a515c1b31" />


## Motivation
I built DEDA Chat to better understand how real-time messaging works under the hood, synchronization between clients, instant message delivery, and terminal-to-terminal communication. The result is a minimal, working foundation that others can use to build their own messenger.

## Features

- Real-time messaging using [Gorilla WebSocket](https://github.com/gorilla/websocket)
- Type-safe database queries via [sqlc](https://sqlc.dev) with migrations managed by [Goose](https://github.com/pressly/goose)
- [JWT](https://github.com/golang-jwt/jwt) — based authentication
- [argon2id](https://github.com/alexedwards/argon2id) — password hashing
- Terminal client — a fully interactive TUI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lipgloss](https://github.com/charmbracelet/lipgloss)
- PostgreSQL — for data storage
- Containerized — runs out of the box with Docker and Docker Compose
- Friend system — users can send, accept, and remove friends
- Notifications — unread message counts and pending friend requests
- Online/offline status tracking
- Chat history
- Health check endpoint for container orchestration readiness


## Try It Now
A public server is running at `https://dedachat-production.up.railway.app`. No setup needed, just run the client and connect:

```bash
> SERVER_ADDR=https://dedachat-production.up.railway.app go run ./cmd/client
```

or via flag

```bash
> go run ./cmd/client --server https://dedachat-production.up.railway.app
```

## Requirements

- [Docker](https://www.docker.com/) 
- [Go 1.27+](https://golang.org/)

## Quick Start

1. Clone and cd to the repository.
```bash
> git clone https://github.com/denisnosik/dedachat
> cd dedachat
```

2. Rename the .env.example file to .env and fill it in with your own values.
```
.env.example contains:

SECRET=your_secret_here
DB_URL=postgres://postgres:postgres@db:5432/messenger?sslmode=disable
GOOSE_DRIVER=postgres
GOOSE_DBSTRING=postgres://postgres:postgres@db:5432/messenger?sslmode=disable
GOOSE_MIGRATION_DIR=sql/schema
```

3. Start the server and database using Docker Compose in detached mode:
```bash
> docker compose up --build -d
```

4. Run the client:
```bash
> go run ./cmd/client
```

Or you can build the client:
```bash
> go build -o messenger ./cmd/client
> ./messenger
```

## Usage/Examples

The client uses the Bubble Tea TUI for visual convenience; just use the commands to navigate through the application.

```
register                       create a new account
login                          sign in using your nickname and password
chat <nickname>                open a chat with a friend
friends <nickname>             send a friend request to a user
friends --delete <nickname>    remove a user from your friends list
friends --list                 display a list of all your friends
notifications                  display unread messages and friend requests
```

### Create a new user and login
To create a new account, enter the command
```bash
>  register
```

After you have a user, you can log in using the command:

```bash
>  login
``` 

### Friendship

<img width="800" height="450" alt="deda_friends_gif-ezgif com-video-to-gif-converter" src="https://github.com/user-attachments/assets/25a21959-1be1-4af8-8647-1f8cf8e9ea42" />

To start chatting, you need to add friends first.

Enter the following command to send a friend request:

```bash
> friends <nickname>
```
You must wait until the other user accepts your friend request.

To remove a user from your friends list, use the `--delete` flag:

```bash
> friends --delete <nickname>
```

To display a list of all your friends, use the `--list` flag:

```bash
> friends --list
```

### Chat

<img width="800" height="450" alt="dedachat_chat_gif-ezgif com-video-to-gif-converter (1)" src="https://github.com/user-attachments/assets/49974c0a-0c92-4d82-8484-a69b06d0256a" />

To open a chat, use the command

```bash
> chat <nickname>
```

After entering the command, a simple chat window will open where you can communicate with your friend.

## Contributing
Contributions are always welcome!

If you have suggestions, ideas, or find any issues, feel free to open an issue or submit a pull request.

## API

### Health
```
GET  /api/health                - check server and database health
```

### Authentication
```
POST /api/register              - create account
POST /api/login                 - sign in, returns JWT token
```

### Friends
```
GET  /api/friends               - get friends list
POST /api/friends               - send friend request
DELETE /api/friends             - remove friend
```

### Chats
```
POST /api/chats                 - create or get existing chat
GET  /api/chats/ws              - connect to chat via WebSocket
POST /api/chats/{chat_id}/read  - mark messages as read
```

### Notifications
```
GET  /api/notifications         - get unread messages and friend requests
```

### Status
```
GET  /api/presence/ws           - hold open to be counted as online
```
The client opens this socket after login and keeps it for the whole session. A
user is online for exactly as long as their socket is alive, so a crash, a lost
network or a killed process drops them within a minute — no explicit "I am
leaving" call is needed, and none can be missed.
