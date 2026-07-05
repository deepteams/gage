package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/deepteams/gage"
)

// isTerminalAbort reports whether err is Bubble Tea's cancellation sentinel, so
// the caller can exit 0 on a normal quit instead of printing a failure.
func isTerminalAbort(err error) bool {
	return errors.Is(err, tea.ErrProgramKilled)
}

type approvalEnvelope struct {
	req  gage.PermissionRequest
	resp chan gage.Approval
}

type questionEnvelope struct {
	question string
	resp     chan string
}

type tuiInteractor struct {
	approvals chan *approvalEnvelope
	questions chan *questionEnvelope
}

func newTUIInteractor() *tuiInteractor {
	return &tuiInteractor{
		approvals: make(chan *approvalEnvelope),
		questions: make(chan *questionEnvelope),
	}
}

func (t *tuiInteractor) AskApproval(ctx context.Context, req gage.PermissionRequest) (gage.Approval, error) {
	env := &approvalEnvelope{req: req, resp: make(chan gage.Approval, 1)}
	select {
	case t.approvals <- env:
	case <-ctx.Done():
		return gage.Approval{}, ctx.Err()
	}
	select {
	case res := <-env.resp:
		return res, nil
	case <-ctx.Done():
		return gage.Approval{}, ctx.Err()
	}
}

func (t *tuiInteractor) AskQuestion(ctx context.Context, question string) (string, error) {
	env := &questionEnvelope{question: question, resp: make(chan string, 1)}
	select {
	case t.questions <- env:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case res := <-env.resp:
		return res, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type runMsg struct {
	Event   *gage.Event
	Done    bool
	Summary string
	Err     error
}

type approvalMsg struct{ env *approvalEnvelope }
type questionMsg struct{ env *questionEnvelope }

type tuiModel struct {
	ctx       context.Context
	app       *appRuntime
	interact  *tuiInteractor
	mode      agentMode
	input     textinput.Model
	viewport  viewport.Model
	lines     []string
	running   bool
	runCh     <-chan runMsg
	approval  *approvalEnvelope
	question  *questionEnvelope
	textBuf   string
	textStart int
	textOpen  bool
	width     int
	lastUsage string
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	modeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	toolStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

func runTUI(ctx context.Context, app *appRuntime, initial agentMode, auto bool) error {
	interact := newTUIInteractor()
	app.SetInteractors(interact, interact, auto)
	ti := textinput.New()
	ti.Placeholder = "Ask for a change, or /help"
	ti.Focus()
	ti.Prompt = "> "
	ti.CharLimit = 8000
	vp := viewport.New(100, 24)
	m := tuiModel{
		ctx:      ctx,
		app:      app,
		interact: interact,
		mode:     initial,
		input:    ti,
		viewport: vp,
		lines: []string{
			dimStyle.Render("Welcome to gage-coding. Try /help, /mode plan, /init, /commands, or ask for a code change."),
		},
	}
	if auto {
		m.lines = append(m.lines, errStyle.Render("⚠ approval prompts disabled (-auto/-yolo): every tool call runs unattended"))
	}
	m.syncViewport()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(waitApproval(m.interact.approvals), waitQuestion(m.interact.questions))
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.viewport.Width = msg.Width
		m.viewport.Height = max(5, msg.Height-7)
		m.input.Width = max(20, msg.Width-4)
		m.syncViewport()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.approval != nil {
			cmd := m.handleApprovalKey(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
		if m.question != nil {
			if msg.Type == tea.KeyEnter {
				answer := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				m.question.resp <- answer
				m.appendLine(toolStyle.Render("? " + m.question.question))
				m.appendLine(dimStyle.Render("answer: " + answer))
				m.question = nil
				cmds = append(cmds, waitQuestion(m.interact.questions))
				return m, tea.Batch(cmds...)
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if msg.Type == tea.KeyEnter {
			cmd := m.submit()
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	case runMsg:
		if msg.Event != nil {
			m.handleEvent(*msg.Event)
			if m.runCh != nil {
				cmds = append(cmds, waitRun(m.runCh))
			}
		}
		if msg.Done {
			m.flushText()
			m.running = false
			m.runCh = nil
			// If the run ended while a question prompt was still pending (e.g.
			// the run was cancelled), clear it so the input box does not stay
			// locked in answer mode, and re-arm the question listener.
			if m.question != nil {
				m.question = nil
				m.input.Placeholder = "Ask for a change, or /help"
				cmds = append(cmds, waitQuestion(m.interact.questions))
			}
			if msg.Err != nil {
				m.appendLine(errStyle.Render("run failed: " + msg.Err.Error()))
			} else if msg.Summary != "" {
				m.lastUsage = msg.Summary
				m.appendLine(dimStyle.Render(msg.Summary))
			}
		}
	case approvalMsg:
		m.approval = msg.env
		m.flushText()
		m.appendLine(toolStyle.Render("approval requested: " + approvalSummary(msg.env.req)))
	case questionMsg:
		m.question = msg.env
		m.flushText()
		m.input.SetValue("")
		m.input.Placeholder = "answer the agent's question"
		m.appendLine(toolStyle.Render("? " + msg.env.question))
	}
	m.syncViewport()
	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	spec := specForMode(m.mode)
	status := fmt.Sprintf("%s  %s  %s", titleStyle.Render("gage-coding"), modeStyle.Render(spec.Label), dimStyle.Render(m.app.modelID))
	if m.running {
		status += "  " + dimStyle.Render("running")
	}
	if m.lastUsage != "" {
		status += "  " + dimStyle.Render(m.lastUsage)
	}
	header := boxStyle.Width(max(20, m.width-2)).Render(status + "\n" + dimStyle.Render(m.app.root))
	footer := dimStyle.Render("ctrl+c quit · /help commands · tab is not needed here: use /mode build|plan|review")
	if m.approval != nil {
		footer = toolStyle.Render("allow? y once · a always for this exact call · n deny")
	}
	if m.question != nil {
		footer = toolStyle.Render("answer the question, then press enter")
	}
	return header + "\n" + m.viewport.View() + "\n" + footer + "\n" + m.input.View()
}

func (m *tuiModel) submit() tea.Cmd {
	line := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.input.Placeholder = "Ask for a change, or /help"
	if line == "" || m.running {
		return nil
	}
	if strings.HasPrefix(line, "/") {
		res, err := m.app.HandleSlash(m.ctx, line, m.mode)
		if err != nil {
			m.appendLine(errStyle.Render(err.Error()))
			return nil
		}
		if res.Quit {
			return tea.Quit
		}
		if res.SetMode != nil {
			m.mode = *res.SetMode
		}
		if res.ClearLog {
			m.lines = nil
		}
		if res.Output != "" {
			m.appendBlock(res.Output)
		}
		if res.Prompt != "" {
			m.appendLine(titleStyle.Render("custom command -> " + string(res.Mode)))
			return m.startRun(res.Prompt, res.Mode)
		}
		return nil
	}
	return m.startRun(line, m.mode)
}

func (m *tuiModel) startRun(prompt string, mode agentMode) tea.Cmd {
	m.running = true
	m.lastUsage = ""
	m.flushText()
	m.appendLine(modeStyle.Render("you ["+string(mode)+"]: ") + prompt)
	ch := make(chan runMsg, 32)
	m.runCh = ch
	ctx := m.ctx
	go func() {
		defer close(ch)
		// Always select on ctx.Done() when sending: if the TUI exits mid-run
		// nothing drains ch, and a bare send on the full buffer would block the
		// goroutine (and the agent run it drives) forever.
		_, summary, err := m.app.RunPrompt(ctx, prompt, mode, func(ev gage.Event) {
			evCopy := ev
			select {
			case ch <- runMsg{Event: &evCopy}:
			case <-ctx.Done():
			}
		})
		select {
		case ch <- runMsg{Done: true, Summary: summary, Err: err}:
		case <-ctx.Done():
		}
	}()
	return waitRun(ch)
}

func (m *tuiModel) handleApprovalKey(msg tea.KeyMsg) tea.Cmd {
	var approval gage.Approval
	switch strings.ToLower(msg.String()) {
	case "y":
		approval = gage.Allowed()
	case "a":
		approval = gage.Approval{Allow: true, Remember: true}
	case "n", "esc":
		approval = gage.Denied("the user denied this call; explain what you wanted to do or try another approach")
	default:
		return nil
	}
	m.approval.resp <- approval
	if approval.Allow {
		m.appendLine(dimStyle.Render("approval: allowed"))
	} else {
		m.appendLine(dimStyle.Render("approval: denied"))
	}
	m.approval = nil
	return waitApproval(m.interact.approvals)
}

func (m *tuiModel) handleEvent(ev gage.Event) {
	switch ev.Type {
	case gage.EventMessageStart:
		m.flushText()
	case gage.EventTextDelta:
		m.streamText(ev.Text)
	case gage.EventReasoningDelta:
		// Keep the demo readable; reasoning streams can be very noisy.
	case gage.EventToolCallDone:
		m.flushText()
		m.appendLine(toolStyle.Render("⏺ "+ev.ToolCall.Name) + " " + dimStyle.Render(toolInputString(ev.ToolCall.Input, 160)))
	case gage.EventToolResult:
		status := "ok"
		if ev.ToolResult.IsError {
			status = "error"
		}
		m.appendLine(dimStyle.Render("  ⎿ " + status + " " + truncate(resultText(ev.ToolResult), 220)))
	case gage.EventMessageDone:
		m.flushText()
	case gage.EventError:
		m.flushText()
		m.appendLine(errStyle.Render("✗ " + ev.Err.Error()))
	}
}

func (m *tuiModel) flushText() {
	if m.textOpen {
		m.renderTextBlock()
	}
	m.textBuf = ""
	m.textStart = 0
	m.textOpen = false
}

func (m *tuiModel) streamText(delta string) {
	if !m.textOpen {
		m.textOpen = true
		m.textStart = len(m.lines)
	}
	m.textBuf += delta
	m.renderTextBlock()
}

func (m *tuiModel) renderTextBlock() {
	if !m.textOpen {
		return
	}
	if m.textStart < 0 || m.textStart > len(m.lines) {
		m.textStart = len(m.lines)
	}
	m.lines = m.lines[:m.textStart]
	text := strings.TrimRight(m.textBuf, "\n")
	parts := strings.Split(text, "\n")
	if len(parts) == 0 {
		parts = []string{""}
	}
	m.lines = append(m.lines, "assistant: "+parts[0])
	for _, line := range parts[1:] {
		m.lines = append(m.lines, line)
	}
}

func (m *tuiModel) appendBlock(s string) {
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		m.appendLine(line)
	}
}

func (m *tuiModel) appendLine(s string) {
	m.lines = append(m.lines, s)
}

func (m *tuiModel) syncViewport() {
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
	m.viewport.GotoBottom()
}

func approvalSummary(req gage.PermissionRequest) string {
	if req.Summary != "" {
		return req.Summary
	}
	return req.Tool + " " + string(req.Input)
}

func waitRun(ch <-chan runMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return runMsg{Done: true}
		}
		return msg
	}
}

func waitApproval(ch <-chan *approvalEnvelope) tea.Cmd {
	return func() tea.Msg {
		env := <-ch
		return approvalMsg{env: env}
	}
}

func waitQuestion(ch <-chan *questionEnvelope) tea.Cmd {
	return func() tea.Msg {
		env := <-ch
		return questionMsg{env: env}
	}
}
