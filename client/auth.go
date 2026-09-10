package client

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type authStep int
type authMode int

const (
	stepNickname authStep = iota
	stepPassword
)

const (
	modeLogin authMode = iota
	modeRegister
)

type authModel struct {
	cfg      *config
	mode     authMode
	step     authStep
	nickname string
	password string
	err      error
}

type switchToAuthMsg struct {
	mode authMode
}

type loginSuccessMsg struct {
	nickname string
	token    string
}

type registerSuccessMsg struct {
	nickname string
}

type loginErrMsg struct{ err error }

var (
	authWelcomeStyle = lipgloss.NewStyle().
				MarginLeft(2).
				Foreground(lipgloss.Color("2")).
				Bold(true)

	authLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			MarginLeft(2)

	authActiveInputStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("255")).
				Padding(0, 1).
				MarginLeft(2).
				Width(40)

	authInactiveInputStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("238")).
				Padding(0, 1).
				MarginLeft(2).
				Width(40)

	authErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			MarginLeft(2)
)

func (m authModel) Init() tea.Cmd {
	return nil
}

func (m authModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case loginErrMsg:
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, func() tea.Msg { return switchToAppMsg{} }

		case "enter":
			if m.step == stepNickname {
				if m.nickname == "" {
					m.err = fmt.Errorf("nickname cannot be empty")
					return m, nil
				}
				m.step = stepPassword
			} else {
				if m.password == "" {
					m.err = fmt.Errorf("password cannot be empty")
					return m, nil
				}

				if m.mode == modeLogin {
					return m, m.doLogin
				}

				return m, m.doRegister
			}

		case "backspace":
			if m.step == stepNickname {
				runes := []rune(m.nickname)
				if len(runes) > 0 {
					m.nickname = string(runes[:len(runes)-1])
				}
			} else {
				runes := []rune(m.password)
				if len(runes) > 0 {
					m.password = string(runes[:len(runes)-1])
				}
			}

		case "ctrl+z":
			if m.step == stepPassword {
				m.step = stepNickname
				m.password = ""
				m.err = nil
			}

		default:
			m.err = nil
			if len(msg.String()) == 1 {
				if m.step == stepNickname {
					m.nickname += msg.String()
				} else {
					m.password += msg.String()
				}
			}
		}
	}

	return m, nil
}

func (m authModel) View() string {
	var b strings.Builder

	title := "Login"
	if m.mode == modeRegister {
		title = "Register"
	}

	b.WriteString("\n")
	b.WriteString(authWelcomeStyle.Render(logo))
	b.WriteString("\n\n")
	b.WriteString(authWelcomeStyle.Render(title))
	b.WriteString("\n\n")

	nicknameBox := ""
	if m.step == stepNickname {
		nicknameBox = authActiveInputStyle.Render(m.nickname + "█")
	} else {
		nicknameBox = authInactiveInputStyle.Render(m.nickname)
	}

	b.WriteString(authLabelStyle.Render("Nickname"))
	b.WriteString("\n")
	b.WriteString(nicknameBox)
	b.WriteString("\n\n")

	if m.step == stepPassword {
		masked := strings.Repeat("*", len([]rune(m.password)))
		passwordBox := authActiveInputStyle.Render(masked + "█")

		b.WriteString(authLabelStyle.Render("Password"))
		b.WriteString("\n")
		b.WriteString(passwordBox)
		b.WriteString("\n\n")
	}

	if m.err != nil {
		b.WriteString(authErrorStyle.Render(m.err.Error()))
		b.WriteString("\n")
	}

	b.WriteString(authLabelStyle.Render("enter — next | ctrl+z back to login input | esc, ctrl+c — quit"))
	b.WriteString("\n")

	return b.String()
}

func (m authModel) doLogin() tea.Msg {
	result, err := m.cfg.client.login(m.nickname, m.password)
	if err != nil {
		return loginErrMsg{fmt.Errorf("couldn't login")}
	}

	if err := m.cfg.presence.Start(result.Token); err != nil {
		return loginErrMsg{fmt.Errorf("couldn't set online")}
	}

	return loginSuccessMsg{
		nickname: result.Nickname,
		token:    result.Token,
	}
}

func (m authModel) doRegister() tea.Msg {
	result, err := m.cfg.client.register(m.nickname, m.password)
	if err != nil {
		return loginErrMsg{fmt.Errorf("couldn't register")}
	}

	return registerSuccessMsg{
		nickname: result.Nickname,
	}
}
