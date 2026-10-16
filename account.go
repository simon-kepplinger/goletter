package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type accountInfo struct {
	Account
	contacts []Contact // fetched when not cached yet
}

// accountScreen shows credentials and sending state. During setup, next
// continues once the credentials work.
type accountScreen struct {
	a    *app
	info accountInfo
	err  error
	next func() (screen, tea.Cmd)
	picker
}

type loginMsg struct{ err error }

// loginCmd runs aws login in the terminal and reports how it went, with the
// CLI's own error message when it has one.
func loginCmd(cfg Config) tea.Cmd {
	cmd, err := loginCommand(cfg)
	if err != nil {
		return func() tea.Msg { return loginMsg{err} }
	}
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if msg := strings.TrimSpace(stderr.String()); err != nil && msg != "" {
			if _, after, ok := strings.Cut(msg, "[ERROR]:"); ok {
				msg = strings.TrimSpace(after)
			}
			err = errors.New(msg)
		}
		return loginMsg{err}
	})
}

func newAccount(a *app, next func() (screen, tea.Cmd)) (screen, tea.Cmd) {
	fetch := a.p.List != "" && a.contacts == nil
	return load(a, "Checking AWS credentials…", func(ctx context.Context) (accountInfo, error) {
		var info accountInfo
		s, err := a.client()
		if err != nil {
			return info, err
		}
		if info.Account, err = s.account(ctx); err != nil {
			return info, err
		}
		if fetch {
			info.contacts, err = s.contacts(ctx)
		}
		return info, err
	}, func(info accountInfo, err error) (screen, tea.Cmd) {
		if info.ID != "" {
			a.acct = info.Account
		}
		if info.contacts != nil {
			a.setContacts(info.contacts)
		}
		return &accountScreen{a: a, info: info, err: err, next: next}, nil
	})
}

// back returns to where the screen was opened from.
func (s *accountScreen) back() (screen, tea.Cmd) {
	if s.next != nil {
		return newSetupForm(s.a, s.a.p.Dir)
	}
	return newMenu(s.a), nil
}

func (s *accountScreen) actions() []action {
	use := action{"Use this account", func() (screen, tea.Cmd) {
		if s.next != nil {
			return s.next()
		}
		return newMenu(s.a), nil
	}}
	if s.err != nil {
		use = action{"Back", s.back}
	}
	acts := []action{use}
	if s.err == nil && !s.info.Tracking {
		acts = append(acts, action{"Turn on open and click tracking", s.enableTracking})
	}
	return append(acts, action{"Log in with another account", func() (screen, tea.Cmd) { return s, loginCmd(s.a.p.Config) }})
}

// enableTracking asks before switching on the paid SES feature behind the
// open and click statistics.
func (s *accountScreen) enableTracking() (screen, tea.Cmd) {
	desc := "This enables Virtual Deliverability Manager in Amazon SES for the whole account. AWS charges $0.07 per 1,000 emails for it. Campaigns sent from then on show how many were opened and clicked."
	return &confirm{
		text: "Turn on open and click tracking?\n" + muted.Width(width).Render(desc),
		yes:  "Yes, turn it on",
		no:   "No, not now",
		cur:  1,
		onNo: func() (screen, tea.Cmd) { return s, nil },
		onYes: func() (screen, tea.Cmd) {
			return load(s.a, "Turning on tracking…", func(ctx context.Context) (struct{}, error) {
				c, err := s.a.client()
				if err != nil {
					return struct{}{}, err
				}
				return struct{}{}, c.enableTracking(ctx)
			}, func(_ struct{}, err error) (screen, tea.Cmd) {
				if err != nil {
					s.err = err
					return s, nil
				}
				return newAccount(s.a, s.next)
			})
		},
	}, nil
}

func (s *accountScreen) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		acts := s.actions()
		switch k := msg.String(); k {
		case "esc":
			return s.back()
		case "r":
			return newAccount(s.a, s.next)
		case "enter":
			return acts[min(s.cur, len(acts)-1)].run()
		default:
			s.move(k, len(acts))
		}
	case loginMsg:
		if msg.err != nil {
			s.err = fmt.Errorf("login failed: %w", msg.err)
			return s, nil
		}
		s.a.ses = nil // pick up the new session
		return newAccount(s.a, s.next)
	}
	return s, nil
}

func (s *accountScreen) view() string {
	var b strings.Builder
	if s.err != nil {
		b.WriteString(notice(s.err) + "\n\n")
	}
	if i := s.info; i.ID != "" {
		sending := good.Render("✓ production")
		if !i.Production {
			sending = warn.Render("sandbox, only verified addresses receive mail")
		}
		tracking := good.Render("✓ opens and clicks are counted")
		if !i.Tracking {
			tracking = warn.Render("off, campaigns get no open and click statistics")
		}
		quota := fmt.Sprintf("%.0f of %.0f sent in the last 24h · max %.0f/s", i.Sent, i.Quota, i.Rate)
		if i.Quota < 0 {
			quota = fmt.Sprintf("%.0f sent in the last 24h · max %.0f/s", i.Sent, i.Rate)
		}
		subs := muted.Render("no contact list configured")
		if s.a.subscribers >= 0 {
			subs = fmt.Sprintf("%d subscribed to %s", s.a.subscribers, s.a.p.listTopic())
		}
		b.WriteString(rows(
			"Account", i.ID,
			"Identity", i.Arn,
			"Sending", sending,
			"Tracking", tracking,
			"Quota", quota,
			"Reputation", i.Status,
			"Subscribers", subs,
		))
	}
	var labels []string
	for _, act := range s.actions() {
		labels = append(labels, act.label)
	}
	b.WriteString("\n" + s.picker.view(labels))
	return b.String()
}

func (s *accountScreen) keys() []string {
	return []string{"Enter to confirm", "r to refresh", "Esc to go back"}
}

// login offers to run aws login, then retries what needed the session.
type login struct {
	a     *app
	why   error // what made a login necessary
	fail  error // the last login attempt, if it failed
	cur   int
	retry func() (screen, tea.Cmd)
}

func newLogin(a *app, why error, retry func() (screen, tea.Cmd)) (screen, tea.Cmd) {
	return &login{a: a, why: why, retry: retry}, nil
}

func (l *login) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k", "down", "j", "tab":
			l.cur = 1 - l.cur
		case "enter":
			if l.cur == 1 {
				return l, tea.Quit
			}
			return l, loginCmd(l.a.p.Config)
		case "esc", "q":
			return l, tea.Quit
		}
	case loginMsg:
		if msg.err != nil {
			l.fail = fmt.Errorf("login failed: %w", msg.err)
			return l, nil
		}
		l.a.ses = nil // pick up the new session
		return l.retry()
	}
	return l, nil
}

func (l *login) view() string {
	text := "You need to be logged in to AWS to continue."
	if !errors.Is(l.why, errNotLoggedIn) {
		text = "Your AWS login has expired, please log in again."
	}
	lines := []string{text, muted.Render("Logging in opens a secure AWS sign-in page in your browser."), ""}
	for i, o := range []string{"Log in", "Exit"} {
		lines = append(lines, option(o, i == l.cur))
	}
	if l.fail != nil {
		lines = append(lines, "", bad.Render(l.fail.Error()))
	}
	return strings.Join(lines, "\n")
}

func (l *login) keys() []string { return []string{"Enter to confirm", "Esc to exit"} }

// subscriberList shows the contact list, held in memory for the session,
// and changes or removes one contact at a time.
type subscriberList struct {
	a   *app
	l   list.Model
	err error
}

type contactItem struct {
	item
	c Contact
}

func (i contactItem) FilterValue() string { return i.c.Email }

func contactRow(c Contact) contactItem {
	state := bad.Render("unsubscribed")
	if c.Subscribed {
		state = good.Render("subscribed")
	}
	return contactItem{item(c.Email + "  " + state), c}
}

func newSubscriberList(a *app) (screen, tea.Cmd) {
	if a.contacts != nil {
		return showSubscribers(a), nil
	}
	return refreshSubscribers(a)
}

// refreshSubscribers fetches the contact list anew.
func refreshSubscribers(a *app) (screen, tea.Cmd) {
	return load(a, "Loading subscribers…", func(ctx context.Context) ([]Contact, error) {
		s, err := a.client()
		if err != nil {
			return nil, err
		}
		return s.contacts(ctx)
	}, func(cs []Contact, err error) (screen, tea.Cmd) {
		if err != nil {
			return &message{err: err, next: func() (screen, tea.Cmd) { return newMenu(a), nil }}, nil
		}
		a.setContacts(cs)
		return showSubscribers(a), nil
	})
}

func showSubscribers(a *app) *subscriberList {
	items := make([]list.Item, len(a.contacts))
	for i, c := range a.contacts {
		items[i] = contactRow(c)
	}
	return &subscriberList{a: a, l: newList(a, items, true)}
}

// toggle asks before flipping the contact's subscription, then updates the
// row in place.
func (s *subscriberList) toggle(index int, c Contact) (screen, tea.Cmd) {
	text := fmt.Sprintf("Unsubscribe %s?\n", c.Email) + muted.Width(width).Render("They stop receiving the newsletter, the same as if they had clicked the unsubscribe link. They can be subscribed again later.")
	yes, no := "Yes, unsubscribe", "No, keep them"
	if !c.Subscribed {
		text = fmt.Sprintf("Subscribe %s again?\n", c.Email) + muted.Width(width).Render("Only do this when they asked you to. People who unsubscribed themselves must not be added back without their consent.")
		yes, no = "Yes, they asked for it", "No, leave it"
	}
	on := !c.Subscribed
	return s.confirm(text, yes, no, "Updating…", func(ctx context.Context, ses *SES) error {
		return ses.setSubscribed(ctx, c.Email, on)
	}, func() tea.Cmd {
		c.Subscribed = on
		s.a.contacts[index] = c
		if on {
			s.a.subscribers++
		} else {
			s.a.subscribers--
		}
		return s.l.SetItem(index, contactRow(c))
	})
}

// remove asks before deleting the contact from the list.
func (s *subscriberList) remove(index int, c Contact) (screen, tea.Cmd) {
	text := fmt.Sprintf("Remove %s from the list?\n", c.Email) + muted.Width(width).Render("Their address is deleted from the contact list in SES. SES then also forgets that they unsubscribed, so a later import could add them back.")
	return s.confirm(text, "Yes, remove them", "No, keep them", "Removing…", func(ctx context.Context, ses *SES) error {
		return ses.deleteContact(ctx, c.Email)
	}, func() tea.Cmd {
		s.a.contacts = slices.Delete(s.a.contacts, index, index+1)
		if c.Subscribed {
			s.a.subscribers--
		}
		s.l.RemoveItem(index)
		fit(&s.l, s.a)
		return nil
	})
}

// confirm asks, runs the change against SES and applies it locally on success.
func (s *subscriberList) confirm(text, yes, no, busy string, do func(context.Context, *SES) error, apply func() tea.Cmd) (screen, tea.Cmd) {
	return &confirm{text: text, yes: yes, no: no, cur: 1,
		onNo: func() (screen, tea.Cmd) { return s, nil },
		onYes: func() (screen, tea.Cmd) {
			return load(s.a, busy, func(ctx context.Context) (struct{}, error) {
				ses, err := s.a.client()
				if err != nil {
					return struct{}{}, err
				}
				return struct{}{}, do(ctx, ses)
			}, func(_ struct{}, err error) (screen, tea.Cmd) {
				if s.err = err; err != nil {
					return s, nil
				}
				return s, apply()
			})
		},
	}, nil
}

func (s *subscriberList) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		fit(&s.l, s.a)
	case tea.KeyPressMsg:
		s.err = nil
		if !s.l.SettingFilter() {
			it, ok := s.l.SelectedItem().(contactItem)
			switch msg.String() {
			case "esc":
				if !s.l.IsFiltered() {
					return newMenu(s.a), nil
				}
			case "r":
				return refreshSubscribers(s.a)
			case "enter":
				if ok {
					return s.toggle(s.l.GlobalIndex(), it.c)
				}
			case "x":
				if ok {
					return s.remove(s.l.GlobalIndex(), it.c)
				}
			}
		}
	}
	var cmd tea.Cmd
	s.l, cmd = s.l.Update(msg)
	return s, cmd
}

func (s *subscriberList) view() string {
	summary := fmt.Sprintf("%d of %d contacts subscribed to %s", s.a.subscribers, len(s.a.contacts), s.a.p.listTopic())
	if s.l.FilterState() != list.Unfiltered && s.l.FilterValue() != "" {
		summary += fmt.Sprintf(" · %d match %q", len(s.l.VisibleItems()), s.l.FilterValue())
	}
	return muted.Render(summary) + "\n" + s.l.View() + "\n" + notice(s.err)
}

func (s *subscriberList) keys() []string {
	return []string{"Enter to change subscription", "x to remove", "/ to filter", "r to refresh", "Esc to go back"}
}
