package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
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
	resp chan approvalDecision
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

func (t *tuiInteractor) AskApproval(ctx context.Context, req gage.PermissionRequest) (approvalDecision, error) {
	env := &approvalEnvelope{req: req, resp: make(chan approvalDecision, 1)}
	select {
	case t.approvals <- env:
	case <-ctx.Done():
		return approvalDecision{}, ctx.Err()
	}
	select {
	case res := <-env.resp:
		return res, nil
	case <-ctx.Done():
		return approvalDecision{}, ctx.Err()
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

type detailBlock struct {
	Title string
	Lines []string
}

type tuiModel struct {
	ctx       context.Context
	app       *appRuntime
	interact  *tuiInteractor
	mode      agentMode
	input     textarea.Model
	viewport  viewport.Model
	lines     []string
	running   bool
	runCh     <-chan runMsg
	cancelRun context.CancelFunc
	approval  *approvalEnvelope
	question  *questionEnvelope
	textBuf   string
	textStart int
	textOpen  bool
	width     int
	lastUsage string
	follow    bool
	history   []string
	histIndex int
	histDraft string
	details   []detailBlock
	toolIDs   map[string]int

	// live-run status
	runStart  time.Time
	spin      int
	tickOn    bool
	runIn     int
	runOut    int
	thinking  bool
	reasonBuf string

	// mouse capture toggle (/mouse): on = wheel scroll, off = native selection
	mouseOn bool

	// hasPending mirrors app.pending for the status bar. The runtime field is
	// written by the run goroutine, so the TUI only re-reads it at
	// happens-before points (run Done, slash results), never from View.
	hasPending bool

	// streamPrefix caches the joined stable lines while a text block streams,
	// so syncViewport does not re-join the whole scrollback on every delta.
	streamPrefix string
}

type tickMsg time.Time

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func tickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
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
	ti := textarea.New()
	ti.Placeholder = "Ask for a change, paste multiple lines, or /help"
	ti.Prompt = "> "
	ti.ShowLineNumbers = false
	ti.CharLimit = 8000
	ti.MaxHeight = 6
	ti.SetHeight(2)
	ti.FocusedStyle.Prompt = titleStyle
	ti.FocusedStyle.Placeholder = dimStyle
	ti.BlurredStyle.Prompt = titleStyle
	ti.BlurredStyle.Placeholder = dimStyle
	ti.Focus()
	vp := viewport.New(100, 24)
	vp.MouseWheelEnabled = true
	history := loadPromptHistory(app.root)
	m := tuiModel{
		ctx:       ctx,
		app:       app,
		interact:  interact,
		mode:      initial,
		input:     ti,
		viewport:  vp,
		follow:    true,
		history:   history,
		histIndex: len(history),
		toolIDs:   map[string]int{},
		mouseOn:   true,
		lines: []string{
			dimStyle.Render("Welcome to gage-coding. Try /help, /mode plan, /init, /commands, or ask for a code change."),
		},
	}
	if auto {
		m.lines = append(m.lines, errStyle.Render("⚠ approval prompts disabled (-auto/-yolo): every tool call runs unattended"))
	}
	if app.pending != nil {
		m.hasPending = true
		m.lines = append(m.lines, toolStyle.Render("a paused run from a previous session awaits decisions — /resume to continue"))
	}
	m.syncViewport()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
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
		m.resizeInput()
		m.viewport.Height = max(5, msg.Height-m.input.Height()-6)
		m.syncViewport()
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		m.follow = m.viewport.AtBottom()
		cmds = append(cmds, cmd)
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
				m.resizeInput()
				m.question.resp <- answer
				m.appendLine(toolStyle.Render("? " + m.question.question))
				m.appendLine(dimStyle.Render("answer: " + answer))
				m.question = nil
				m.input.Placeholder = "Ask for a change, paste multiple lines, or /help"
				cmds = append(cmds, waitQuestion(m.interact.questions))
				return m, tea.Batch(cmds...)
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.resizeInput()
			return m, cmd
		}
		if m.handleViewportKey(msg) {
			return m, nil
		}
		switch {
		case m.running && (msg.String() == "ctrl+x" || msg.String() == "esc"):
			m.cancelCurrentRun()
			return m, nil
		case msg.String() == "tab":
			// Swallow tab whether or not a completion applied: a literal tab
			// character in the prompt is never what the user meant here.
			m.completeInput()
			return m, nil
		case msg.String() == "alt+enter" || msg.Type == tea.KeyCtrlJ:
			m.input.InsertString("\n")
			m.resizeInput()
			return m, nil
		case msg.String() == "up" && m.input.LineCount() <= 1:
			m.historyPrev()
			return m, nil
		case msg.String() == "down" && m.input.LineCount() <= 1:
			m.historyNext()
			return m, nil
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
		m.resizeInput()
		cmds = append(cmds, cmd)
	case tickMsg:
		if m.running {
			m.spin++
			cmds = append(cmds, tickCmd())
		} else {
			m.tickOn = false
		}
	case runMsg:
		if msg.Event != nil {
			m.handleEvent(*msg.Event)
			if m.runCh != nil {
				cmds = append(cmds, waitRun(m.runCh))
			}
		}
		if msg.Done {
			m.flushText()
			m.flushReasoning()
			m.running = false
			m.runCh = nil
			m.cancelRun = nil
			// The run goroutine is done (Done is its last send), so reading the
			// runtime's pending checkpoint is race-free here.
			m.hasPending = m.app.pending != nil
			// If the run ended while a question prompt was still pending (e.g.
			// the run was cancelled), clear it so the input box does not stay
			// locked in answer mode, and re-arm the question listener.
			if m.question != nil {
				m.question = nil
				m.input.Placeholder = "Ask for a change, paste multiple lines, or /help"
				cmds = append(cmds, waitQuestion(m.interact.questions))
			}
			if errors.Is(msg.Err, context.Canceled) {
				m.appendLine(dimStyle.Render("run cancelled"))
			} else if msg.Err != nil {
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
		if preview := approvalPreview(msg.env.req); preview != "" {
			m.appendBlock(preview)
		}
	case questionMsg:
		m.question = msg.env
		m.flushText()
		m.input.SetValue("")
		m.resizeInput()
		m.input.Placeholder = "answer the agent's question"
		m.appendLine(toolStyle.Render("? " + msg.env.question))
	}
	m.syncViewport()
	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	spec := specForMode(m.mode)
	status := fmt.Sprintf("%s  %s  %s", titleStyle.Render("gage-coding"), modeStyle.Render(spec.Label), dimStyle.Render(m.app.modelID))
	if m.app.sessionID != "" {
		status += "  " + dimStyle.Render("session "+m.app.sessionID)
	}
	if m.running {
		frame := spinnerFrames[m.spin%len(spinnerFrames)]
		run := fmt.Sprintf("%s %s", frame, time.Since(m.runStart).Round(time.Second))
		if m.runIn > 0 || m.runOut > 0 {
			run += fmt.Sprintf(" · %d in / %d out tok", m.runIn, m.runOut)
		}
		if m.thinking {
			run += " · thinking…"
		}
		status += "  " + toolStyle.Render(run)
	}
	if m.hasPending && !m.running {
		status += "  " + toolStyle.Render("paused · /resume")
	}
	if m.lastUsage != "" {
		status += "  " + dimStyle.Render(m.lastUsage)
	}
	if len(m.details) > 0 {
		status += "  " + dimStyle.Render(fmt.Sprintf("%d detail(s)", len(m.details)))
	}
	header := boxStyle.Width(max(20, m.width-2)).Render(status + "\n" + dimStyle.Render(m.app.root))
	footer := m.footer()
	if m.approval != nil {
		footer = toolStyle.Render("allow? y once · a always exact input · t always this tool · p postpone (pause run) · n deny")
	}
	if m.question != nil {
		footer = toolStyle.Render("answer the question, then press enter")
	}
	return header + "\n" + m.viewport.View() + "\n" + footer + "\n" + m.input.View()
}

func (m *tuiModel) submit() tea.Cmd {
	line := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.resizeInput()
	m.input.Placeholder = "Ask for a change, paste multiple lines, or /help"
	if line == "" {
		return nil
	}
	if strings.EqualFold(line, "/cancel") {
		m.cancelCurrentRun()
		return nil
	}
	if m.running {
		m.appendLine(dimStyle.Render("run already in progress; use /cancel or ctrl+x to stop it"))
		return nil
	}
	m.rememberPrompt(line)
	if strings.HasPrefix(line, "/") {
		if handled, cmd := m.handleLocalSlash(line); handled {
			return cmd
		}
		res, err := m.app.HandleSlash(m.ctx, line, m.mode)
		// No run is in flight here (checked above), so the runtime's pending
		// checkpoint is safe to read after /clear or /resume mutated it.
		m.hasPending = m.app.pending != nil
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
			m.follow = true
		}
		if res.Output != "" {
			m.appendBlock(res.Output)
		}
		if res.Resume {
			return m.startResume()
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
	m.appendPrompt(prompt, mode)
	return m.launch(func(ctx context.Context, emit func(gage.Event)) (string, error) {
		_, summary, err := m.app.RunPrompt(ctx, prompt, mode, emit)
		return summary, err
	})
}

func (m *tuiModel) startResume() tea.Cmd {
	m.appendLine(titleStyle.Render("resuming paused run"))
	mode := m.mode
	return m.launch(func(ctx context.Context, emit func(gage.Event)) (string, error) {
		_, summary, err := m.app.RunResume(ctx, mode, emit)
		return summary, err
	})
}

// launch runs one agent invocation in a goroutine and streams its events back
// into the Update loop.
func (m *tuiModel) launch(run func(ctx context.Context, emit func(gage.Event)) (string, error)) tea.Cmd {
	m.running = true
	m.lastUsage = ""
	m.runStart = time.Now()
	m.runIn, m.runOut = 0, 0
	m.flushText()
	ch := make(chan runMsg, 32)
	m.runCh = ch
	parentCtx := m.ctx
	ctx, cancel := context.WithCancel(parentCtx)
	m.cancelRun = cancel
	go func() {
		defer close(ch)
		// Always select on ctx.Done() when sending: if the TUI exits mid-run
		// nothing drains ch, and a bare send on the full buffer would block the
		// goroutine (and the agent run it drives) forever.
		summary, err := run(ctx, func(ev gage.Event) {
			evCopy := ev
			select {
			case ch <- runMsg{Event: &evCopy}:
			case <-ctx.Done():
			}
		})
		select {
		case ch <- runMsg{Done: true, Summary: summary, Err: err}:
		case <-parentCtx.Done():
		}
	}()
	cmds := []tea.Cmd{waitRun(ch)}
	if !m.tickOn {
		m.tickOn = true
		cmds = append(cmds, tickCmd())
	}
	return tea.Batch(cmds...)
}

func (m *tuiModel) handleApprovalKey(msg tea.KeyMsg) tea.Cmd {
	var dec approvalDecision
	var note string
	switch strings.ToLower(msg.String()) {
	case "y":
		dec = approvalDecision{Approval: gage.Allowed()}
		note = "approval: allowed"
	case "a":
		dec = approvalDecision{Approval: gage.Approval{Allow: true, Remember: true}}
		note = "approval: allowed (remembered for this exact call)"
	case "t":
		dec = approvalDecision{Approval: gage.Allowed(), RememberTool: true}
		note = "approval: allowed (remembered for every " + m.approval.req.Tool + " call this session)"
	case "p":
		dec = approvalDecision{Postpone: true}
		note = "approval: postponed — the run will pause; /resume to decide later"
	case "n", "esc":
		dec = approvalDecision{Approval: gage.Denied("the user denied this call; explain what you wanted to do or try another approach")}
		note = "approval: denied"
	default:
		return nil
	}
	m.approval.resp <- dec
	m.appendLine(dimStyle.Render(note))
	m.approval = nil
	return waitApproval(m.interact.approvals)
}

func (m *tuiModel) handleEvent(ev gage.Event) {
	switch ev.Type {
	case gage.EventMessageStart:
		m.flushText()
	case gage.EventTextDelta:
		m.flushReasoning()
		m.streamText(ev.Text)
	case gage.EventReasoningDelta:
		// Reasoning is too noisy to stream inline, but not worthless: show a
		// "thinking…" status and keep the full text expandable via /detail.
		if m.textOpen {
			m.flushText()
		}
		m.thinking = true
		m.reasonBuf += ev.Text
	case gage.EventReasoningDone:
		m.flushReasoning()
	case gage.EventUsage:
		if ev.Usage != nil {
			m.runIn += ev.Usage.InputTokens
			m.runOut += ev.Usage.OutputTokens
		}
	case gage.EventToolCallDone:
		m.flushText()
		m.flushReasoning()
		if ev.ToolCall == nil {
			return
		}
		id := m.addDetail("tool "+ev.ToolCall.Name,
			"input:",
			indent(prettyJSON(ev.ToolCall.Input)),
		)
		if ev.ToolCall.ID != "" {
			if m.toolIDs == nil {
				m.toolIDs = map[string]int{}
			}
			m.toolIDs[ev.ToolCall.ID] = id
		}
		m.appendLine(toolStyle.Render(fmt.Sprintf("[%d] %s", id, ev.ToolCall.Name)) + " " + dimStyle.Render(toolInputString(ev.ToolCall.Input, 160)))
	case gage.EventToolResult:
		if ev.ToolResult == nil {
			return
		}
		status := "ok"
		if ev.ToolResult.IsError {
			status = "error"
		}
		id := m.detailIDForResult(ev.ToolResult)
		m.appendDetail(id,
			"",
			"result:",
			indent(resultText(ev.ToolResult)),
		)
		m.appendLine(dimStyle.Render(fmt.Sprintf("  [%d] %s %s", id, status, truncate(resultText(ev.ToolResult), 220))))
	case gage.EventMessageDone:
		m.flushText()
		m.flushReasoning()
	case gage.EventError:
		m.flushText()
		m.appendLine(errStyle.Render("✗ " + ev.Err.Error()))
	}
}

// flushReasoning closes an accumulated reasoning block: the full text becomes
// an expandable detail, and a one-line pointer lands in the scrollback.
func (m *tuiModel) flushReasoning() {
	m.thinking = false
	buf := strings.TrimSpace(m.reasonBuf)
	m.reasonBuf = ""
	if buf == "" {
		return
	}
	id := m.addDetail("reasoning", indent(limitRunes(buf, 8000)))
	m.appendLine(dimStyle.Render(fmt.Sprintf("[%d] reasoning (%d chars) · /detail %d", id, len([]rune(buf)), id)))
}

func (m *tuiModel) flushText() {
	if m.textOpen {
		// The message is complete: replace the plain streamed block with the
		// markdown-styled render.
		m.setTextBlock(renderMarkdown(strings.TrimRight(m.textBuf, "\n"), m.wrapWidth()))
	}
	m.textBuf = ""
	m.textStart = 0
	m.textOpen = false
	m.streamPrefix = ""
}

func (m *tuiModel) streamText(delta string) {
	if !m.textOpen {
		m.textOpen = true
		m.textStart = len(m.lines)
		m.streamPrefix = strings.Join(m.lines, "\n")
	}
	m.textBuf += delta
	m.renderTextBlock()
}

// renderTextBlock re-renders the streaming text block as plain wrapped text;
// markdown styling is applied once, on flush, when the message is complete.
func (m *tuiModel) renderTextBlock() {
	if !m.textOpen {
		return
	}
	width := m.wrapWidth()
	var parts []string
	for _, line := range strings.Split(strings.TrimRight(m.textBuf, "\n"), "\n") {
		parts = append(parts, wrapLine(line, width)...)
	}
	m.setTextBlock(parts)
}

func (m *tuiModel) setTextBlock(parts []string) {
	if m.textStart < 0 || m.textStart > len(m.lines) {
		m.textStart = len(m.lines)
	}
	m.lines = m.lines[:m.textStart]
	if len(parts) == 0 {
		parts = []string{""}
	}
	m.lines = append(m.lines, "assistant: "+parts[0])
	m.lines = append(m.lines, parts[1:]...)
}

func (m *tuiModel) wrapWidth() int {
	if m.width <= 0 {
		return 98
	}
	return max(20, m.width-2)
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
	var content string
	if m.textOpen && m.textStart >= 0 && m.textStart <= len(m.lines) {
		// While a text block streams, only its own tail changes: join the small
		// tail and reuse the cached prefix instead of re-joining the whole
		// scrollback on every delta.
		tail := strings.Join(m.lines[m.textStart:], "\n")
		switch {
		case m.streamPrefix == "":
			content = tail
		case tail == "":
			content = m.streamPrefix
		default:
			content = m.streamPrefix + "\n" + tail
		}
	} else {
		content = strings.Join(m.lines, "\n")
	}
	m.viewport.SetContent(content)
	if m.follow {
		m.viewport.GotoBottom()
	}
}

func (m *tuiModel) resizeInput() {
	if m.width > 0 {
		m.input.SetWidth(max(20, m.width-4))
	}
	height := max(2, min(6, m.input.LineCount()))
	if m.question != nil {
		height = 2
	}
	m.input.SetHeight(height)
}

func (m tuiModel) footer() string {
	parts := []string{"enter send", "alt+enter newline", "tab complete", "up/down history", "pgup/pgdn scroll", "/detail last"}
	if m.running {
		parts = append(parts, "ctrl+x cancel")
	}
	if !m.follow {
		parts = append(parts, "output paused")
	}
	if !m.mouseOn {
		parts = append(parts, "mouse off (/mouse)")
	}
	if hint := m.completionHint(); hint != "" {
		parts = append(parts, hint)
	}
	return dimStyle.Render(strings.Join(parts, " · "))
}

func (m *tuiModel) handleViewportKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "pgup":
		m.viewport.PageUp()
		m.follow = false
		return true
	case "pgdown":
		m.viewport.PageDown()
		m.follow = m.viewport.AtBottom()
		return true
	default:
		return false
	}
}

func (m *tuiModel) completeInput() bool {
	value := m.input.Value()
	matches := m.completionMatches(value)
	if len(matches) == 0 {
		return false
	}
	next := longestCommonPrefix(matches)
	if len([]rune(next)) <= len([]rune(value)) {
		next = matches[0]
	}
	if next == value {
		return false
	}
	m.input.SetValue(next)
	m.input.CursorEnd()
	m.resizeInput()
	return true
}

func (m tuiModel) completionHint() string {
	value := m.input.Value()
	matches := m.completionMatches(value)
	if len(matches) == 0 {
		return ""
	}
	hint := matches[0]
	if len(matches) > 1 {
		hint = fmt.Sprintf("%s (+%d)", hint, len(matches)-1)
	}
	return "tab -> " + truncate(hint, 48)
}

func (m tuiModel) completionMatches(value string) []string {
	if strings.Contains(value, "\n") || !strings.HasPrefix(value, "/") {
		return nil
	}
	var out []string
	for _, candidate := range m.completionCandidates() {
		if strings.HasPrefix(candidate, value) && candidate != value {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

func (m tuiModel) completionCandidates() []string {
	candidates := []string{
		"/?", "/cancel", "/clear", "/commands", "/detail last", "/details list",
		"/help", "/init", "/mode build", "/mode plan", "/mode review",
		"/mouse", "/quit", "/redo", "/reload", "/resume", "/sessions", "/skills",
		"/tools build", "/tools plan", "/tools review", "/undo",
	}
	for _, cmd := range m.app.commands.List() {
		candidates = append(candidates, "/"+cmd.Name+" ")
	}
	seen := map[string]bool{}
	unique := candidates[:0]
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		unique = append(unique, candidate)
	}
	sort.Strings(unique)
	return unique
}

func longestCommonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, value := range values[1:] {
		for !strings.HasPrefix(value, prefix) {
			if prefix == "" {
				return ""
			}
			prefix = string([]rune(prefix)[:len([]rune(prefix))-1])
		}
	}
	return prefix
}

func (m *tuiModel) rememberPrompt(prompt string) {
	if len(m.history) == 0 || m.history[len(m.history)-1] != prompt {
		m.history = append(m.history, prompt)
	}
	if len(m.history) > promptHistoryLimit {
		m.history = m.history[len(m.history)-promptHistoryLimit:]
	}
	m.histIndex = len(m.history)
	m.histDraft = ""
	savePromptHistory(m.app.root, m.history)
}

func (m *tuiModel) historyPrev() {
	if len(m.history) == 0 {
		return
	}
	if m.histIndex < 0 || m.histIndex > len(m.history) {
		m.histIndex = len(m.history)
	}
	if m.histIndex == len(m.history) {
		m.histDraft = m.input.Value()
	}
	if m.histIndex > 0 {
		m.histIndex--
	}
	m.input.SetValue(m.history[m.histIndex])
	m.input.CursorEnd()
	m.resizeInput()
}

func (m *tuiModel) historyNext() {
	if len(m.history) == 0 || m.histIndex < 0 || m.histIndex >= len(m.history) {
		return
	}
	if m.histIndex < len(m.history)-1 {
		m.histIndex++
		m.input.SetValue(m.history[m.histIndex])
	} else {
		m.histIndex = len(m.history)
		m.input.SetValue(m.histDraft)
		m.histDraft = ""
	}
	m.input.CursorEnd()
	m.resizeInput()
}

func (m *tuiModel) cancelCurrentRun() {
	if !m.running || m.cancelRun == nil {
		m.appendLine(dimStyle.Render("no run to cancel"))
		return
	}
	m.cancelRun()
	m.appendLine(dimStyle.Render("cancelling current run..."))
}

func (m *tuiModel) handleLocalSlash(line string) (bool, tea.Cmd) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, nil
	}
	name := strings.TrimPrefix(fields[0], "/")
	args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	switch name {
	case "cancel":
		m.cancelCurrentRun()
		return true, nil
	case "detail", "details":
		m.showDetail(args)
		return true, nil
	case "mouse":
		m.mouseOn = !m.mouseOn
		if m.mouseOn {
			m.appendLine(dimStyle.Render("mouse capture on: wheel scrolls the output, terminal text selection off"))
			return true, tea.EnableMouseCellMotion
		}
		m.appendLine(dimStyle.Render("mouse capture off: select/copy text natively; pgup/pgdn still scroll"))
		return true, tea.DisableMouse
	default:
		return false, nil
	}
}

func (m *tuiModel) showDetail(args string) {
	args = strings.TrimSpace(args)
	if len(m.details) == 0 {
		m.appendLine("no tool details yet")
		return
	}
	if args == "list" {
		for i, d := range m.details {
			m.appendLine(fmt.Sprintf("[%d] %s", i+1, d.Title))
		}
		return
	}
	id := len(m.details)
	if args != "" && args != "last" {
		parsed, err := strconv.Atoi(args)
		if err != nil || parsed < 1 || parsed > len(m.details) {
			m.appendLine(errStyle.Render(fmt.Sprintf("unknown detail %q", args)))
			return
		}
		id = parsed
	}
	block := m.details[id-1]
	m.appendLine(titleStyle.Render(fmt.Sprintf("detail [%d] %s", id, block.Title)))
	for _, line := range block.Lines {
		m.appendLine(line)
	}
}

func (m *tuiModel) appendPrompt(prompt string, mode agentMode) {
	parts := strings.Split(prompt, "\n")
	if len(parts) == 0 {
		return
	}
	m.appendLine(modeStyle.Render("you ["+string(mode)+"]: ") + parts[0])
	for _, line := range parts[1:] {
		m.appendLine("  " + line)
	}
}

func (m *tuiModel) addDetail(title string, lines ...string) int {
	m.details = append(m.details, detailBlock{Title: title, Lines: cleanDetailLines(lines)})
	return len(m.details)
}

func (m *tuiModel) appendDetail(id int, lines ...string) {
	if id < 1 || id > len(m.details) {
		return
	}
	m.details[id-1].Lines = append(m.details[id-1].Lines, cleanDetailLines(lines)...)
}

func (m *tuiModel) detailIDForResult(tr *gage.ToolResult) int {
	if tr != nil {
		if id, ok := m.toolIDs[tr.CallID]; ok {
			return id
		}
		return m.addDetail("tool result", "call_id: "+tr.CallID)
	}
	return m.addDetail("tool result")
}

func cleanDetailLines(lines []string) []string {
	var out []string
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(strings.TrimRight(line, "\n"), "\n")...)
	}
	return out
}

func approvalPreview(req gage.PermissionRequest) string {
	switch req.Tool {
	case "bash":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(req.Input, &args); err == nil && args.Command != "" {
			return "command:\n" + indent(args.Command)
		}
	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(req.Input, &args); err == nil {
			return fmt.Sprintf("path: %s\ncontent preview:\n%s", args.Path, indent(limitRunes(args.Content, 1200)))
		}
	case "edit":
		var args struct {
			Path       string `json:"path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := json.Unmarshal(req.Input, &args); err == nil {
			if diff, ok := lineDiff(args.OldString, args.NewString, 400); ok {
				return fmt.Sprintf("path: %s\nreplace_all: %v\ndiff:\n%s",
					args.Path, args.ReplaceAll, indent(diff))
			}
			return fmt.Sprintf("path: %s\nreplace_all: %v\nold:\n%s\nnew:\n%s",
				args.Path, args.ReplaceAll,
				indent(limitRunes(args.OldString, 800)),
				indent(limitRunes(args.NewString, 800)))
		}
	}
	if len(req.Input) == 0 {
		return ""
	}
	return "input:\n" + indent(limitRunes(prettyJSON(req.Input), 1200))
}

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func indent(s string) string {
	if s == "" {
		return "  (empty)"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

func limitRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
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
