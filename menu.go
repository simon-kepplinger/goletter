package main

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type menu struct {
	a   *app
	cur int
}

type entry struct {
	label string
	run   func(*menu) (screen, tea.Cmd)
	gap   bool              // starts a new group
	note  func(*app) string // muted text after the label
}

var entries = []entry{
	{label: "Campaigns", run: func(m *menu) (screen, tea.Cmd) { return newCampaignList(m.a) },
		note: func(a *app) string { return strconv.Itoa(a.p.campaignCount()) }},
	{label: "Subscribers", run: func(m *menu) (screen, tea.Cmd) { return newSubscriberList(m.a) },
		note: func(a *app) string {
			if a.subscribers < 0 {
				return ""
			}
			return strconv.Itoa(a.subscribers)
		}},
	{label: "New campaign", run: func(m *menu) (screen, tea.Cmd) { return newCampaignForm(m.a) }, gap: true},
	{label: "SES Dashboard ↗", run: console("account"), gap: true},
	{label: "Deliverability ↗", run: console("vdm/dashboard")},
	{label: "Suppression list ↗", run: console("suppression-list")},
	{label: "Account", run: func(m *menu) (screen, tea.Cmd) { return newAccount(m.a, nil) }, gap: true, note: whoami},
	{label: "Quit", run: func(m *menu) (screen, tea.Cmd) { return m, tea.Quit }, gap: true},
}

func newMenu(a *app) *menu { return &menu{a: a} }

// whoami is the signed-in account and user, once known.
func whoami(a *app) string {
	if a.acct.ID == "" {
		return ""
	}
	user := a.acct.Arn[strings.LastIndex(a.acct.Arn, ":")+1:]
	user = user[strings.LastIndex(user, "/")+1:]
	return a.acct.ID + " · " + user
}

// console opens a page of the SES console in the browser.
func console(page string) func(*menu) (screen, tea.Cmd) {
	return func(m *menu) (screen, tea.Cmd) {
		m.a.notify("opened in the browser", openURL(m.a.p.consoleURL(page)))
		return m, nil
	}
}

func (m *menu) update(msg tea.Msg) (screen, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "up", "k":
			m.cur = max(m.cur-1, 0)
		case "down", "j":
			m.cur = min(m.cur+1, len(entries)-1)
		case "enter":
			return entries[m.cur].run(m)
		case "q":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *menu) view() string {
	lines := []string{accent.Bold(true).Render("go") + bold.Render("letter"), ""}
	for i, e := range entries {
		if e.gap {
			lines = append(lines, "")
		}
		line := option(e.label, i == m.cur)
		if e.note != nil {
			if n := e.note(m.a); n != "" {
				line += muted.Render(" " + n)
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *menu) keys() []string { return []string{"Enter to open", "q to quit"} }
