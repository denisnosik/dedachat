package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
	"uuid"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
)

type wsMessage struct {
	Nickname  string    `json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
	Content   string    `json:"content"`
}

type chatModel struct {
	messages        []string
	input           string
	msgChan         chan wsMessage
	conn            *websocket.Conn
	ctx             context.Context
	cancel          context.CancelFunc
	currentNickname string
	width           int
	height          int
	scrollOffset    int
	msgBoxHeight    int
	err             error
}

type switchToChatMsg struct {
	chatID          uuid.UUID
	token           string
	currentNickname string
}

const maxInputLen = 150

var (
	chatContainerStyle = lipgloss.NewStyle().
				Padding(1, 2)

	chatTimeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	chatNicknameStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("6")).
				Bold(true)

	chatYouStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("2")).
			Bold(true)

	chatCounterStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Italic(true)

	chatHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	chatMessageBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("255")).
				Padding(0, 1)

	chatInputBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("255")).
				Padding(0, 1)

	chatErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			MarginLeft(2)
)

type incomingMsg wsMessage

func (m chatModel) Init() tea.Cmd {
	return waitForMessage(m.ctx, m.msgChan)
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		const reservedLines = 7

		m.msgBoxHeight = max(msg.Height-reservedLines, 3)

		return m, nil

	case incomingMsg:
		formatted := formatMessage(wsMessage(msg), m.currentNickname)
		m.messages = append(m.messages, formatted)
		return m, waitForMessage(m.ctx, m.msgChan)

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if m.input != "" {
				if err := m.conn.WriteMessage(websocket.TextMessage, []byte(m.input)); err != nil {
					m.err = fmt.Errorf("couldn't send message: %w", err)
					return m, nil
				}

				formatted := formatMessage(wsMessage{
					Nickname:  m.currentNickname,
					CreatedAt: time.Now(),
					Content:   m.input,
				}, m.currentNickname)

				m.messages = append(m.messages, formatted)
				m.input = ""
			}

		case "backspace":
			runes := []rune(m.input)
			if len(runes) > 0 {
				m.input = string(runes[:len(runes)-1])
			}

		case "up":
			if m.scrollOffset < len(m.messages)-m.msgBoxHeight {
				m.scrollOffset++
			}

		case "down":
			if m.scrollOffset > 0 {
				m.scrollOffset--
			}

		case "ctrl+c", "esc":
			return m, func() tea.Msg { return switchToAppMsg{} }

		default:
			if len([]rune(m.input)) < maxInputLen && len(msg.String()) == 1 {
				m.input += msg.String()
			}
		}
	}

	return m, nil
}

func (m chatModel) View() string {
	width := m.width
	if width < 40 {
		width = 80
	}

	innerWidth := width - 8

	const reservedLines = 7
	msgBoxHeight := max(m.height-reservedLines, 3)

	total := len(m.messages)
	end := max(total-m.scrollOffset, 0)

	start := max(end-msgBoxHeight, 0)

	visible := m.messages[start:end]

	var b strings.Builder

	padding := msgBoxHeight - len(visible)
	if padding > 0 {
		b.WriteString(strings.Repeat("\n", padding))
	}

	for _, msg := range visible {
		b.WriteString(msg)
		b.WriteString("\n")
	}

	msgs := b.String()
	if msgs == "" {
		msgs = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Italic(true).
			Render("No messages yet...")
	}

	scrollIndicator := ""
	if m.scrollOffset > 0 {
		scrollIndicator = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Render(fmt.Sprintf(" ↑ %d more", m.scrollOffset))
	}

	messagesBox := chatMessageBoxStyle.
		Width(innerWidth).
		Height(msgBoxHeight).
		Render(msgs)

	counter := fmt.Sprintf("%d/%d", len([]rune(m.input)), maxInputLen)
	counterLine := lipgloss.NewStyle().
		Width(innerWidth - 2).
		Align(lipgloss.Right).
		Render(chatCounterStyle.Render(counter))

	inputBox := chatInputBoxStyle.
		Width(innerWidth).
		Render("> " + m.input + "█\n" + counterLine)

	if m.err != nil {
		return chatErrorStyle.Render("Error: " + m.err.Error())
	}

	help := chatHelpStyle.Render("enter — send  |  ↑↓ — scroll  |  esc/ctrl+c — quit")

	parts := []string{messagesBox}
	if scrollIndicator != "" {
		parts = append(parts, scrollIndicator)
	}
	parts = append(parts, "", inputBox, help)

	return chatContainerStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, parts...),
	)
}

func (c *Client) connectToChat(chatID uuid.UUID, token string, currentNickname string) (*websocket.Conn, context.Context, context.CancelFunc, chan wsMessage, error) {
	wsURL := fmt.Sprintf("%s/api/chats/ws?chat_id=%s&token=%s", wsBaseURL(), chatID, url.QueryEscape(token))

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	msgChan := make(chan wsMessage, 50)

	// messages reader
	go func() {
		defer cancel()

		send := func(msg wsMessage) bool {
			select {
			case msgChan <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				send(wsMessage{Content: "Disconnected"})
				return
			}

			wsMsg := wsMessage{}
			if err := json.Unmarshal(msg, &wsMsg); err != nil {
				send(wsMessage{Content: string(msg)})
				continue
			}

			if !send(wsMsg) {
				return
			}
		}
	}()

	// throttle markAsRead
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.markAsRead(chatID, token); err != nil {
					log.Printf("Couldn't mark as read: %v", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return conn, ctx, cancel, msgChan, nil
}

func formatMessage(msg wsMessage, currentNickname string) string {
	ts := chatTimeStyle.Render("[" + msg.CreatedAt.Format("02 Jan 15:04") + "]")

	isYou := msg.Nickname == currentNickname

	var nick string
	if isYou {
		nick = chatYouStyle.Render("[You]")
	} else {
		nick = chatNicknameStyle.Render("[" + msg.Nickname + "]")
	}

	return ts + " " + nick + " " + msg.Content
}

func waitForMessage(ctx context.Context, ch chan wsMessage) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return incomingMsg(msg)
		case <-ctx.Done():
			return nil
		}
	}
}
