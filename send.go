package main

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
)

// sendPlan is everything a send run needs, computed up front.
type sendPlan struct {
	mail       Mail
	to         []string // subscribers not delivered to yet
	done       int      // subscribers already delivered to
	batch      int
	interval   time.Duration
	production bool
	tracking   bool // opens and clicks will be counted
}

func (a *app) planSend(ctx context.Context, c *Campaign) (sendPlan, error) {
	s, err := a.client()
	if err != nil {
		return sendPlan{}, err
	}
	mail, err := a.p.render(c)
	if err != nil {
		return sendPlan{}, err
	}
	acc, err := s.account(ctx)
	if err != nil {
		return sendPlan{}, err
	}
	subs, err := s.subscribers(ctx)
	if err != nil {
		return sendPlan{}, err
	}
	log, err := a.p.readLog(c.Slug)
	if err != nil {
		return sendPlan{}, err
	}
	if err := s.ensureConfigSet(ctx, configSet(c.Slug)); err != nil {
		return sendPlan{}, err
	}
	plan := sendPlan{mail: mail, production: acc.Production, tracking: acc.Tracking}
	for _, e := range subs {
		k, err := a.p.pseudonym(e)
		if err != nil {
			return sendPlan{}, err
		}
		if log[k].Status == statusSuccess {
			plan.done++
		} else {
			plan.to = append(plan.to, e)
		}
	}
	plan.batch, plan.interval = batchPlan(acc.Rate)
	return plan, nil
}

// duration says roughly how long the send will take.
func (p sendPlan) duration() string {
	batches := (len(p.to) + p.batch - 1) / p.batch
	if d := time.Duration(batches) * p.interval; d >= 90*time.Second {
		return fmt.Sprintf("the next %d minutes", int(d.Minutes()+0.5))
	}
	return "the next minute"
}

// batchPlan sizes SendBulkEmail calls so that recipients per second stay
// within the account's maximum send rate.
func batchPlan(rate float64) (int, time.Duration) {
	if rate < 1 {
		return 1, time.Second
	}
	size := min(50, int(rate))
	return size, time.Duration(float64(size) / rate * float64(time.Second))
}

// newSendConfirm counts recipients and asks for confirmation.
func newSendConfirm(a *app, c *Campaign) (screen, tea.Cmd) {
	return load(a, "Counting subscribers…", func(ctx context.Context) (sendPlan, error) {
		return a.planSend(ctx, c)
	}, func(plan sendPlan, err error) (screen, tea.Cmd) {
		d, cmd := openDetail(a, c)
		back := func() (screen, tea.Cmd) { return d, nil }
		if err != nil {
			a.notify("", err)
			return d, cmd
		}
		if len(plan.to) == 0 {
			a.notify(fmt.Sprintf("nothing to send, all %d subscribers already received it", plan.done), nil)
			return d, cmd
		}
		desc := fmt.Sprintf("Sent from %s to %s. It goes out automatically over %s, you can watch the progress.", a.p.From, a.p.listTopic(), plan.duration())
		if plan.done > 0 {
			desc += fmt.Sprintf(" %d subscribers already received it and are skipped.", plan.done)
		}
		if !plan.production {
			desc += " Your account is still in the SES sandbox, so only verified addresses receive mail."
		}
		if !plan.tracking {
			desc += " Open and click tracking is off, so this campaign will have no such statistics."
		}
		return &confirm{
			text:  fmt.Sprintf("Do you want to send %q to %d subscribers?\n", c.Subject, len(plan.to)) + muted.Width(width).Render(desc),
			yes:   "Yes, send it",
			no:    "No, not yet",
			cur:   1,
			onYes: func() (screen, tea.Cmd) { return newSending(a, c, plan) },
			onNo:  back,
		}, nil
	})
}

// sending runs the batches one after another, logging every result.
type sending struct {
	a          *app
	c          *Campaign
	plan       sendPlan
	pb         progress.Model
	i          int // next recipient index
	ok, failed int
	lastErr    string
	err        error
	stop       bool
}

type batchMsg struct {
	recs []sendRecord
	err  error
}

type nextMsg struct{}

type doneMsg struct {
	sent, failed int
	err          error
}

func newSending(a *app, c *Campaign, plan sendPlan) (screen, tea.Cmd) {
	s := &sending{a: a, c: c, plan: plan, pb: progress.New(progress.WithColors(blue))}
	s.pb.SetWidth(min(a.w, 60))
	return s, s.sendNext()
}

func (s *sending) sendNext() tea.Cmd {
	if s.stop || s.err != nil || s.i >= len(s.plan.to) {
		return s.finish()
	}
	to := s.plan.to[s.i:min(s.i+s.plan.batch, len(s.plan.to))]
	ses, slug, mail := s.a.ses, s.c.Slug, s.plan.mail
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		recs, err := ses.sendBatch(ctx, slug, mail, to)
		return batchMsg{recs, err}
	}
}

// finish folds the whole log into delivered and failed counts.
func (s *sending) finish() tea.Cmd {
	p, slug := s.a.p, s.c.Slug
	return func() tea.Msg {
		log, err := p.readLog(slug)
		if err != nil {
			return doneMsg{err: err}
		}
		var m doneMsg
		for _, r := range log {
			if r.Status == statusSuccess {
				m.sent++
			} else {
				m.failed++
			}
		}
		return m
	}
}

func (s *sending) update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "esc" {
			s.stop = true
		}
	case batchMsg:
		if err := s.a.p.appendLog(s.c.Slug, msg.recs); err != nil {
			s.err = err
			return s, s.finish()
		}
		for _, r := range msg.recs {
			if r.Status == statusSuccess {
				s.ok++
			} else {
				s.failed++
				s.lastErr = r.Error
			}
		}
		s.i += len(msg.recs)
		if msg.err != nil {
			s.err = msg.err
		}
		if s.i >= len(s.plan.to) || s.stop || s.err != nil {
			return s, s.finish()
		}
		return s, tea.Tick(s.plan.interval, func(time.Time) tea.Msg { return nextMsg{} })
	case nextMsg:
		return s, s.sendNext()
	case doneMsg:
		if msg.err == nil && msg.sent+msg.failed > 0 {
			now := time.Now()
			s.c.SentAt, s.c.Sent, s.c.Failed = &now, msg.sent, msg.failed
			msg.err = s.a.p.saveCampaign(s.c)
		}
		d, cmd := openDetail(s.a, s.c)
		const resume = `choose "Send to new and failed subscribers" to resume`
		var text string
		err := msg.err
		switch {
		case err != nil: // saving the outcome failed, that comes first
		case s.err != nil:
			err = fmt.Errorf("stopped after %d of %d: %w, %s", s.i, len(s.plan.to), s.err, resume)
		case s.i < len(s.plan.to):
			text = fmt.Sprintf("stopped after %d of %d, %s", s.i, len(s.plan.to), resume)
		case s.failed > 0:
			err = fmt.Errorf("sent to %d, %d failed, last error: %s (details in %s)",
				s.ok, s.failed, s.lastErr, s.a.p.logPath(s.c.Slug))
		default:
			text = fmt.Sprintf("sent to %d subscribers", s.ok)
		}
		s.a.notify(text, err)
		return d, cmd
	}
	return s, nil
}

func (s *sending) view() string {
	total := len(s.plan.to)
	status := ""
	if s.stop {
		status = muted.Render("stopping after this batch…")
	}
	return fmt.Sprintf("Sending %s\n\n%s\n%d of %d sent, %d failed\n%s",
		bold.Render(s.c.Subject), s.pb.ViewAs(float64(s.i)/float64(total)), s.ok, total, s.failed, status)
}

func (s *sending) keys() []string { return []string{"Esc to stop after this batch"} }
