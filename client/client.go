package client

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var baseURL string

type Client struct {
	httpClient http.Client
}

type CurrentUser struct {
	Nickname string
	Token    string
}

type config struct {
	client      Client
	currentUser *CurrentUser
	presence    *presence
}

type clientModel struct {
	cfg     *config
	current tea.Model
	width   int
	height  int
}

var logo = `
██████╗ ███████╗██████╗  █████╗ 
██╔══██╗██╔════╝██╔══██╗██╔══██╗
██║  ██║█████╗  ██║  ██║███████║
██║  ██║██╔══╝  ██║  ██║██╔══██║
██████╔╝███████╗██████╔╝██║  ██║
╚═════╝ ╚══════╝╚═════╝ ╚═╝  ╚═╝
                                
 ██████╗██╗  ██╗ █████╗ ████████╗
██╔════╝██║  ██║██╔══██╗╚══██╔══╝
██║     ███████║███████║   ██║   
██║     ██╔══██║██╔══██║   ██║   
╚██████╗██║  ██║██║  ██║   ██║   
 ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝ 
	`

func (m clientModel) Init() tea.Cmd {
	return m.current.Init()
}

func (m clientModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case appErrMsg:
		var cmd tea.Cmd
		m.current, cmd = m.current.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		m.current, cmd = m.current.Update(msg)
		return m, cmd

	case loginSuccessMsg:
		auth, ok := m.current.(authModel)
		if !ok {
			return m, nil
		}

		auth.cfg.currentUser.Nickname = msg.nickname
		auth.cfg.currentUser.Token = msg.token

		next := appModel{
			cfg: auth.cfg,
		}

		m.current = next
		return m, next.Init()

	case registerSuccessMsg:
		auth, ok := m.current.(authModel)
		if !ok {
			return m, nil
		}

		next := appModel{
			cfg:    auth.cfg,
			output: fmt.Sprintf("registered as %s, now login", msg.nickname),
		}

		m.current = next
		return m, next.Init()

	case switchToAuthMsg:
		app, ok := m.current.(appModel)
		if !ok {
			return m, nil
		}

		next := authModel{
			cfg:  app.cfg,
			mode: msg.mode,
		}

		m.current = next
		return m, next.Init()

	case switchToChatMsg:
		app, ok := m.current.(appModel)
		if !ok {
			return m, nil
		}
		conn, ctx, cancel, msgChan, err := app.cfg.client.connectToChat(msg.chatID, msg.token, msg.currentNickname)
		if err != nil {
			var cmd tea.Cmd
			m.current, cmd = m.current.Update(appErrMsg{err})
			return m, cmd
		}

		next := chatModel{
			msgChan:         msgChan,
			conn:            conn,
			ctx:             ctx,
			cancel:          cancel,
			width:           m.width,
			height:          m.height,
			msgBoxHeight:    m.height - 7,
			currentNickname: msg.currentNickname,
		}

		m.current = next
		return m, next.Init()

	case switchToAppMsg:
		if chat, ok := m.current.(chatModel); ok {
			chat.cancel()
			if err := chat.conn.Close(); err != nil {
				var cmd tea.Cmd
				m.current, cmd = m.current.Update(appErrMsg{err})
				return m, cmd
			}
		}

		next := appModel{cfg: m.cfg}
		m.current = next
		return m, next.Init()
	}

	var cmd tea.Cmd
	m.current, cmd = m.current.Update(msg)
	return m, cmd
}

func (m clientModel) View() string {
	return m.current.View()
}

func Run() {
	serverFlag := flag.String("server", "", "server address")
	flag.Parse()

	server := *serverFlag

	if server == "" {
		server = os.Getenv("SERVER_ADDR")
		if server == "" {
			server = "http://localhost:8080"
		}
	}

	baseURL = server

	client := Client{httpClient: http.Client{Timeout: 5 * time.Second}}

	cfg := &config{
		client:      client,
		currentUser: &CurrentUser{},
		presence:    newPresence(),
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)
	go func() {
		<-c
		cfg.presence.Stop()
		os.Exit(0)
	}()

	p := tea.NewProgram(clientModel{
		cfg:     cfg,
		current: appModel{cfg: cfg},
	}, tea.WithAltScreen())

	_, err := p.Run()
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	// closing the presence socket is what sets the user offline
	cfg.presence.Stop()
}
