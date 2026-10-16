package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/paginator"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// app is the state shared by all screens.
type app struct {
	p           *Project
	ses         *SES
	acct        Account   // the signed-in account, once looked up
	contacts    []Contact // the contact list, kept in memory for the session
	subscribers int       // subscriber count for the menu, -1 until known
	w, h        int       // space available to a screen above the help line
	toast       toast     // the notification below the help line, if any
}

// setContacts keeps the contact list for the session and counts subscribers.
func (a *app) setContacts(cs []Contact) {
	a.contacts, a.subscribers = cs, 0
	for _, c := range cs {
		if c.Subscribed {
			a.subscribers++
		}
	}
}

// screen is one full-screen view; update returns the screen to show next,
// keys lists the key/action pairs for the help line.
type screen interface {
	update(tea.Msg) (screen, tea.Cmd)
	view() string
	keys() []string
}

type root struct {
	a   *app
	s   screen
	cmd tea.Cmd
}

func run(dir string) error {
	r := root{a: &app{subscribers: -1}}
	p, err := openProject(dir)
	switch {
	case errors.Is(err, errNotInitialized):
		r.s, r.cmd = newConfirmInit(r.a, dir)
	case err != nil:
		return err
	default:
		r.a.p = p
		start := func() (screen, tea.Cmd) { return newMenu(r.a), fetchAccount(r.a) }
		r.s, r.cmd = start()
		if _, err := loginConfig(context.Background(), p.Config); needsLogin(err) {
			r.s, r.cmd = newLogin(r.a, err, start)
		}
	}
	_, err = tea.NewProgram(r).Run()
	return err
}

func (r root) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, r.cmd) }

func (r root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.a.w, r.a.h = max(msg.Width-4, 20), max(msg.Height-4, 5)
		width = r.a.w
	case tea.BackgroundColorMsg:
		setTheme(msg.IsDark())
	case accountMsg:
		r.a.acct = msg.Account
		if msg.contacts != nil {
			r.a.setContacts(msg.contacts)
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return r, tea.Quit
		}
		if time.Since(r.a.toast.at) > 3*time.Second {
			r.a.toast = toast{}
		}
	}
	var cmd tea.Cmd
	r.s, cmd = r.s.update(msg)
	return r, cmd
}

var frame = lipgloss.NewStyle().Padding(1, 2)

// width is the space a screen has for a line of text, for wrapping prose.
var width = 76

func (r root) View() tea.View {
	s := strings.TrimRight(r.s.view(), "\n") + "\n\n" + help(r.s.keys()...)
	if t := r.a.toast.view(); t != "" {
		s += "\n\n" + t
	}
	v := tea.NewView(frame.Render(s))
	v.AltScreen = true
	v.WindowTitle = "goletter"
	return v
}

// client returns the SES client, creating it on first use.
func (a *app) client() (*SES, error) {
	if a.ses == nil {
		s, err := newSES(context.Background(), a.p.Config)
		if err != nil {
			return nil, err
		}
		a.ses = s
	}
	return a.ses, nil
}

type accountMsg struct {
	Account
	contacts []Contact // nil when not fetched
}

// fetchAccount looks up the signed-in account and the contact list for the
// menu in the background, with its own client so it does not race the
// screens.
func fetchAccount(a *app) tea.Cmd {
	cfg := a.p.Config
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s, err := newSES(ctx, cfg)
		if err != nil {
			return nil
		}
		acct, err := s.account(ctx)
		if err != nil {
			return nil
		}
		msg := accountMsg{Account: acct}
		if cfg.List != "" {
			msg.contacts, _ = s.contacts(ctx)
		}
		return msg
	}
}

// help renders the key hints, e.g. "Enter to confirm · Esc to cancel".
func help(hints ...string) string {
	return muted.Render(strings.Join(hints, " · "))
}

// notice renders an error, or nothing.
func notice(err error) string {
	if err != nil {
		return bad.Render(err.Error())
	}
	return ""
}

// toast is a notification below the help line. The next key clears it, but
// not within the first seconds, so quick typing does not wipe it unread.
type toast struct {
	text string
	err  error
	at   time.Time
}

// notify shows err, or text when err is nil.
func (a *app) notify(text string, err error) {
	if err != nil {
		text = err.Error()
	}
	if text != "" {
		a.toast = toast{text, err, time.Now()}
	}
}

func (t toast) view() string {
	switch {
	case t.err != nil:
		return bad.Render("✗  " + t.text)
	case t.text != "":
		return note.Render("ℹ  " + t.text)
	}
	return ""
}

// rows renders aligned label/value lines.
func rows(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, "%s  %s\n", muted.Width(12).Render(kv[i]), kv[i+1])
	}
	return b.String()
}

// item is a single-line list entry.
type item string

func (i item) Title() string       { return string(i) }
func (i item) Description() string { return "" }
func (i item) FilterValue() string { return string(i) }

func newList(a *app, items []list.Item, filter bool) list.Model {
	l := list.New(items, delegate(), a.w, 1)
	l.Styles.Filter.Cursor.Color = blue
	l.Styles.Filter.Focused.Prompt = accent
	l.Styles.Filter.Blurred.Prompt = accent
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(filter)
	l.Filter = substringFilter
	l.Paginator.Type = paginator.Arabic // "3/50" instead of a row of dots
	l.Styles.ArabicPagination = muted
	fit(&l, a)
	return l
}

// substringFilter matches case-insensitive substrings. Fuzzy matching, the
// list's default, feels random on email addresses.
func substringFilter(term string, targets []string) []list.Rank {
	term = strings.ToLower(term)
	var ranks []list.Rank
	for i, t := range targets {
		if at := strings.Index(strings.ToLower(t), term); at >= 0 {
			idx := make([]int, len(term))
			for k := range idx {
				idx[k] = at + k
			}
			ranks = append(ranks, list.Rank{Index: i, MatchedIndexes: idx})
		}
	}
	return ranks
}

// fit sizes a list to its items so the help line sits right below them,
// with a page indicator once the items do not fit.
func fit(l *list.Model, a *app) {
	h := len(l.Items())
	if l.FilteringEnabled() {
		h += 2 // filter bar
	}
	h = max(min(h, a.h-4), 1)
	l.SetShowPagination(false)
	l.SetSize(a.w, h)
	if l.Paginator.TotalPages > 1 {
		l.SetShowPagination(true)
		l.SetSize(a.w, h)
	}
}

// loader runs fn in the background behind a spinner, then hands the result to next.
type loader[T any] struct {
	a     *app
	sp    spinner.Model
	text  string
	next  func(T, error) (screen, tea.Cmd)
	retry func() (screen, tea.Cmd)
}

type loadedMsg[T any] struct {
	v   T
	err error
}

func load[T any](a *app, text string, fn func(context.Context) (T, error), next func(T, error) (screen, tea.Cmd)) (screen, tea.Cmd) {
	l := &loader[T]{a: a, sp: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(accent)), text: text, next: next}
	l.retry = func() (screen, tea.Cmd) { return load(a, text, fn, next) }
	return l, tea.Batch(l.sp.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		v, err := fn(ctx)
		return loadedMsg[T]{v, err}
	})
}

func (l *loader[T]) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case loadedMsg[T]:
		if needsLogin(msg.err) { // not an error: offer to log in, then try again
			return newLogin(l.a, msg.err, l.retry)
		}
		return l.next(msg.v, msg.err)
	case spinner.TickMsg:
		var cmd tea.Cmd
		l.sp, cmd = l.sp.Update(msg)
		return l, cmd
	}
	return l, nil
}

func (l *loader[T]) view() string   { return l.sp.View() + " " + l.text }
func (l *loader[T]) keys() []string { return nil }

// message shows an error and waits for a key.
type message struct {
	err  error
	next func() (screen, tea.Cmd)
}

func (m *message) update(msg tea.Msg) (screen, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		return m.next()
	}
	return m, nil
}

func (m *message) view() string   { return notice(m.err) }
func (m *message) keys() []string { return []string{"Enter to continue"} }

// confirm asks a question with two selectable answers.
type confirm struct {
	text    string
	yes, no string // answer labels
	cur     int
	onYes   func() (screen, tea.Cmd)
	onNo    func() (screen, tea.Cmd)
}

func (c *confirm) update(msg tea.Msg) (screen, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "up", "k", "down", "j", "tab":
			c.cur = 1 - c.cur
		case "enter":
			if c.cur == 0 {
				return c.onYes()
			}
			return c.onNo()
		case "y":
			return c.onYes()
		case "n", "esc":
			return c.onNo()
		}
	}
	return c, nil
}

func (c *confirm) view() string {
	lines := []string{c.text, ""}
	for i, o := range []string{c.yes, c.no} {
		lines = append(lines, option(o, i == c.cur))
	}
	return strings.Join(lines, "\n")
}

func (c *confirm) keys() []string { return []string{"Enter to confirm", "Esc to cancel"} }

// picker is a cursor over labelled choices, optionally split into groups.
type picker struct {
	gaps map[int]bool // indexes that start a new group
	cur  int
}

// move handles the navigation keys and reports whether key was one.
func (p *picker) move(key string, n int) bool {
	switch key {
	case "up", "k":
		p.cur = max(p.cur-1, 0)
	case "down", "j":
		p.cur = min(p.cur+1, n-1)
	default:
		return false
	}
	return true
}

func (p *picker) view(labels []string) string {
	var lines []string
	for i, l := range labels {
		if p.gaps[i] && i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, option(l, i == p.cur))
	}
	return strings.Join(lines, "\n")
}

// editor suspends the TUI for $EDITOR on a file, then continues with next.
type editor struct{ next func(error) (screen, tea.Cmd) }

func editFile(path string, next func(error) (screen, tea.Cmd)) (screen, tea.Cmd) {
	return &editor{next}, tea.ExecProcess(editorCmd(path), func(err error) tea.Msg { return editedMsg{err} })
}

func (e *editor) update(msg tea.Msg) (screen, tea.Cmd) {
	if m, ok := msg.(editedMsg); ok {
		return e.next(m.err)
	}
	return e, nil
}

func (e *editor) view() string   { return "" }
func (e *editor) keys() []string { return nil }

// option renders a selectable line, highlighted with a caret when selected.
func option(text string, selected bool) string {
	if selected {
		return accent.Render("❯ " + text)
	}
	return "  " + text
}

// OS integration.

func openURL(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

func editorCmd(path string) *exec.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
		if runtime.GOOS == "windows" {
			ed = "notepad"
		}
	}
	parts := strings.Fields(ed)
	return exec.Command(parts[0], append(parts[1:], path)...)
}
