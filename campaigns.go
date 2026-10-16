package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type campaignItem struct {
	item
	c *Campaign
}

type campaignList struct {
	a   *app
	l   list.Model
	err error
}

func newCampaignList(a *app) (screen, tea.Cmd) {
	cs, err := a.p.campaigns()
	items := make([]list.Item, len(cs))
	for i, c := range cs {
		items[i] = campaignItem{item(c.Subject + "  " + muted.Render(describe(c))), c}
	}
	return &campaignList{a: a, l: newList(a, items, false), err: err}, nil
}

func describe(c *Campaign) string {
	switch c.status() {
	case "draft":
		return "draft"
	case "partial":
		return fmt.Sprintf("sent %s to %d, %d failed", c.SentAt.Format("2006-01-02"), c.Sent, c.Failed)
	default:
		return fmt.Sprintf("sent %s to %d", c.SentAt.Format("2006-01-02"), c.Sent)
	}
}

func (s *campaignList) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		fit(&s.l, s.a)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return newMenu(s.a), nil
		case "n":
			return newCampaignForm(s.a)
		case "enter":
			if it, ok := s.l.SelectedItem().(campaignItem); ok {
				return openDetail(s.a, it.c)
			}
		}
	}
	var cmd tea.Cmd
	s.l, cmd = s.l.Update(msg)
	return s, cmd
}

func (s *campaignList) view() string { return s.l.View() + "\n" + notice(s.err) }
func (s *campaignList) keys() []string {
	return []string{"Enter to open", "n for a new campaign", "Esc to go back"}
}

// detail shows one campaign and what can be done with it.
type detail struct {
	a        *app
	c        *Campaign
	stats    *Stats // last known statistics of a sent campaign
	statsErr error
	picker
}

type editedMsg struct{ err error }

type statsMsg struct {
	slug  string
	stats Stats
	err   error
}

func newDetail(a *app, c *Campaign) *detail {
	d := &detail{a: a, c: c, picker: picker{gaps: map[int]bool{1: true, 3: true, 5: true}}}
	if st, ok := a.p.cachedStats(c.Slug); ok {
		d.stats = &st
	}
	return d
}

// openDetail shows the campaign and, when it was sent, refreshes its statistics.
func openDetail(a *app, c *Campaign) (*detail, tea.Cmd) {
	d := newDetail(a, c)
	return d, d.loadStats()
}

// loadStats fetches the statistics in the background with its own client.
func (d *detail) loadStats() tea.Cmd {
	if d.c.SentAt == nil {
		return nil
	}
	cfg, slug, since := d.a.p.Config, d.c.Slug, d.c.created()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, err := newSES(ctx, cfg)
		if err != nil {
			return statsMsg{slug: slug, err: err}
		}
		st, err := s.stats(ctx, slug, since)
		return statsMsg{slug, st, err}
	}
}

type action struct {
	label string
	run   func() (screen, tea.Cmd)
}

func (d *detail) actions() []action {
	send := "Send campaign"
	if d.c.SentAt != nil {
		send = "Send to new and failed subscribers"
	}
	acts := []action{
		{send, func() (screen, tea.Cmd) { return newSendConfirm(d.a, d.c) }},
		{"Edit content", func() (screen, tea.Cmd) { return d, d.edit() }},
		{"Change subject", func() (screen, tea.Cmd) {
			subject := text("").def(d.c.Subject).must()
			return ask("What should the subject line be?", "This is what subscribers see in their inbox.", subject, func() (screen, tea.Cmd) {
				d.c.Subject = subject.val()
				d.a.notify("", d.a.p.saveCampaign(d.c))
				return d, nil
			}, func() (screen, tea.Cmd) { return d, nil })
		}},
		{"Preview in browser", func() (screen, tea.Cmd) {
			d.a.notify("preview opened in the browser", d.a.preview(d.c))
			return d, nil
		}},
		{"Send a test email", func() (screen, tea.Cmd) { return newTestForm(d.a, d.c) }},
	}
	if d.c.SentAt == nil {
		acts = append(acts, action{"Delete campaign", func() (screen, tea.Cmd) {
			return &confirm{
				text: fmt.Sprintf("Delete %q?\n", d.c.Subject) + muted.Render("The campaign and its content are removed for good."),
				yes:  "Yes, delete it",
				no:   "No, keep it",
				cur:  1,
				onYes: func() (screen, tea.Cmd) {
					if err := d.a.p.deleteCampaign(d.c); err != nil {
						d.a.notify("", err)
						return d, nil
					}
					return newCampaignList(d.a)
				},
				onNo: func() (screen, tea.Cmd) { return d, nil },
			}, nil
		}})
	}
	return acts
}

func (d *detail) edit() tea.Cmd {
	path := d.a.p.campaignFile(d.c.Slug, "body.html")
	return tea.ExecProcess(editorCmd(path), func(err error) tea.Msg { return editedMsg{err} })
}

func (d *detail) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		acts := d.actions()
		switch k := msg.String(); k {
		case "esc":
			return newCampaignList(d.a)
		case "enter":
			return acts[d.cur].run()
		default:
			d.move(k, len(acts))
		}
	case editedMsg:
		if c, err := d.a.p.campaign(d.c.Slug); err == nil {
			d.c = c
		}
		d.a.notify("", msg.err)
	case statsMsg:
		if msg.slug != d.c.Slug {
			break
		}
		if d.statsErr = msg.err; msg.err == nil {
			d.stats = &msg.stats
			d.statsErr = d.a.p.saveStats(d.c.Slug, msg.stats)
		}
	}
	return d, nil
}

// statsView lists what SES counted for the campaign so far.
func (d *detail) statsView() string {
	st := d.stats
	switch {
	case st == nil && d.statsErr != nil:
		return rows("Statistics", muted.Render("unavailable: "+d.statsErr.Error()))
	case st == nil:
		return rows("Statistics", muted.Render("loading…"))
	}
	pct := func(n, of int64) string {
		if of == 0 {
			return ""
		}
		return fmt.Sprintf(" · %.1f%%", 100*float64(n)/float64(of))
	}
	opened := fmt.Sprintf("%d%s", st.Opened, pct(st.Opened, st.Delivered))
	clicked := fmt.Sprintf("%d%s", st.Clicked, pct(st.Clicked, st.Delivered))
	if a := d.a.acct; a.ID != "" && !a.Tracking {
		opened = muted.Render("not counted, turn on tracking under Account")
		clicked = opened
	}
	note := ""
	if d.statsErr != nil {
		note = muted.Render(" (from " + st.FetchedAt.Local().Format("2006-01-02 15:04") + ", refresh failed)")
	}
	return rows(
		"Delivered", fmt.Sprintf("%d of %d sent%s%s", st.Delivered, st.Sent, pct(st.Delivered, st.Sent), note),
		"Opened", opened,
		"Clicked", clicked,
		"Bounced", fmt.Sprintf("%d · %d complaints", st.Bounced, st.Complaints),
	)
}

func (d *detail) view() string {
	c := d.c
	status := describe(c)
	if c.status() == "partial" {
		status = bad.Render(status)
	}
	var labels []string
	for _, a := range d.actions() {
		labels = append(labels, a.label)
	}
	out := rows(
		"Subject", bold.Render(c.Subject),
		"Status", status,
		"Template", cmp.Or(c.Template, muted.Render("none")),
		"Folder", "campaigns/"+c.Slug+"/",
	)
	if c.SentAt != nil {
		out += "\n" + d.statsView()
	}
	return out + "\n" + d.picker.view(labels)
}

func (d *detail) keys() []string { return []string{"Enter to select", "Esc to go back"} }

// preview writes the rendered mail to a temp file and opens it in the browser.
func (a *app) preview(c *Campaign) error {
	m, err := a.p.render(c)
	if err != nil {
		return err
	}
	return show("goletter-"+c.Slug, m.HTML)
}

// previewTemplate opens the page a new campaign would start with.
func (a *app) previewTemplate(subject, template string) error {
	page, err := a.p.wrap(template, scaffold(subject, template))
	if err != nil {
		return err
	}
	return show("goletter-template-"+cmp.Or(template, "none"), page)
}

// show writes html to a temporary file and opens it in the browser.
func show(name, html string) error {
	path := filepath.Join(os.TempDir(), name+".html")
	if err := os.WriteFile(path, []byte(html), 0o600); err != nil {
		return err
	}
	return openURL(path)
}

// newCampaignForm asks for subject and template, scaffolds the campaign and opens the editor.
func newCampaignForm(a *app) (screen, tea.Cmd) { return askSubject(a, "") }

func askSubject(a *app, subject string) (screen, tea.Cmd) {
	f := text("").def(subject).must()
	return ask("What is the subject line of this newsletter?", "This is what subscribers see in their inbox. You can still change it later from the campaign screen.", f, func() (screen, tea.Cmd) {
		return askTemplate(a, f.val(), a.p.Template)
	}, func() (screen, tea.Cmd) { return newMenu(a), nil })
}

const (
	noTemplate  = "Continue without a template"
	newTemplate = "Create a new template"
)

// askTemplate picks the template for a campaign with subject, offering to
// preview each and to create a new one.
func askTemplate(a *app, subject, selected string) (screen, tea.Cmd) {
	templates := a.p.templates()
	opts := append(append([]string{noTemplate}, templates...), newTemplate)
	tpl := choice(opts, cmp.Or(selected, noTemplate)).gap(1, len(opts)-1)
	back := func() (screen, tea.Cmd) { return askSubject(a, subject) }
	return newPrompt("Which design should the newsletter use?", "A template from the project's templates folder adds header, footer and styling around the content you write. Choose none to write the complete email yourself.", tpl, func() (screen, tea.Cmd) {
		t := tpl.val()
		switch t {
		case newTemplate:
			return askTemplateName(a, subject)
		case noTemplate:
			t = ""
		}
		c, err := a.p.newCampaign(subject, t)
		if err != nil {
			return &message{err: err, next: func() (screen, tea.Cmd) { return newMenu(a), nil }}, nil
		}
		d := newDetail(a, c)
		return d, d.edit()
	}, back).key("p", "p to preview", func(p *prompt) (screen, tea.Cmd) {
		t := tpl.val()
		if t == newTemplate {
			return p, nil
		}
		if t == noTemplate {
			t = ""
		}
		a.notify("preview opened in the browser", a.previewTemplate(subject, t))
		return p, nil
	}).start()
}

// askTemplateName creates a template as a copy of Simple, opens it in the
// editor and returns to the template choice with it selected.
func askTemplateName(a *app, subject string) (screen, tea.Cmd) {
	name := text("").must()
	back := func() (screen, tea.Cmd) { return askTemplate(a, subject, "") }
	return ask("What should the new template be called?", "A short name like Announcement. It starts as a copy of the Simple template and opens in your editor.", name, func() (screen, tea.Cmd) {
		n := strings.Map(func(r rune) rune {
			if strings.ContainsRune(`/\:*?"<>|`, r) {
				return '-'
			}
			return r
		}, name.val())
		path := a.p.path("templates", n+".html")
		if err := writeIfMissing(path, simpleTemplate); err != nil {
			return &message{err: err, next: back}, nil
		}
		return editFile(path, func(err error) (screen, tea.Cmd) {
			if err != nil {
				return &message{err: err, next: back}, nil
			}
			return askTemplate(a, subject, n)
		})
	}, back)
}

// newTestForm picks a test recipient from recent addresses or input, then
// sends the test.
func newTestForm(a *app, c *Campaign) (screen, tea.Cmd) {
	back := func() (screen, tea.Cmd) { return newDetail(a, c), nil }
	send := func(to string) (screen, tea.Cmd) {
		return load(a, "Sending test…", func(ctx context.Context) (struct{}, error) {
			s, err := a.client()
			if err != nil {
				return struct{}{}, err
			}
			m, err := a.p.render(c)
			if err != nil {
				return struct{}{}, err
			}
			if err := s.sendTest(ctx, c.Slug, m, to); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, a.p.rememberTest(to)
		}, func(_ struct{}, err error) (screen, tea.Cmd) {
			a.notify("test sent to "+to, err)
			return newDetail(a, c), nil
		})
	}
	const question = "Which address should receive the test email?"
	const desc = "The subject is marked with [test] and the unsubscribe link does nothing, so nobody mistakes it for the real send."
	addr := text("").must()
	askAddr := func(back func() (screen, tea.Cmd)) (screen, tea.Cmd) {
		return ask(question, desc, addr, func() (screen, tea.Cmd) { return send(addr.val()) }, back)
	}
	recent := a.p.testHistory()
	if len(recent) == 0 {
		return askAddr(back)
	}
	const other = "Use new address"
	pick := choice(append([]string{other}, recent...), "").gap(1)
	var p *prompt
	p = newPrompt(question, desc, pick, func() (screen, tea.Cmd) {
		if pick.val() == other {
			return askAddr(func() (screen, tea.Cmd) { return p, nil })
		}
		return send(pick.val())
	}, back)
	return p.start()
}
