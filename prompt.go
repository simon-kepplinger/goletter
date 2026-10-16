package main

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// field is the answer to a prompt: free text or one choice.
type field struct {
	kind     int
	required bool
	gaps     map[int]bool // choice indexes that start a new group
	fresh    bool         // prefilled and untouched: the next typed text replaces it
	in       textinput.Model
	opts     []string
	cur      int
}

const (
	fText = iota
	fChoice
)

func text(value string) *field {
	in := textinput.New()
	in.Prompt = ""
	in.SetValue(value)
	s := textinput.DefaultStyles(isDark)
	s.Cursor.Color = blue
	s.Focused.Suggestion = muted
	in.SetStyles(s)
	return &field{in: in}
}

// def prefills value; typing replaces it, editing keys keep it.
func (f *field) def(value string) *field {
	f.in.SetValue(value)
	f.fresh = true
	return f
}

// suggest completes the text inline from opts; tab accepts, up/down cycle.
func (f *field) suggest(opts []string) *field {
	f.in.ShowSuggestions = true
	f.in.SetSuggestions(opts)
	return f
}

func choice(opts []string, value string) *field {
	return &field{kind: fChoice, opts: opts, cur: max(slices.Index(opts, value), 0)}
}

// gap puts a blank line before the options at the given indexes.
func (f *field) gap(at ...int) *field {
	f.gaps = map[int]bool{}
	for _, i := range at {
		f.gaps[i] = true
	}
	return f
}

func (f *field) must() *field { f.required = true; return f }

func (f *field) val() string {
	switch f.kind {
	case fText:
		return strings.TrimSpace(f.in.Value())
	case fChoice:
		if len(f.opts) > 0 {
			return f.opts[f.cur]
		}
	}
	return ""
}

// prompt asks one question; enter calls done, esc calls back.
type prompt struct {
	question, desc string
	f              *field
	err            string // validation failure, shown under the field
	done, back     func() (screen, tea.Cmd)
	extra          map[string]func(*prompt) (screen, tea.Cmd) // more keys, for choices
	hints          []string
}

func ask(question, desc string, f *field, done, back func() (screen, tea.Cmd)) (screen, tea.Cmd) {
	return newPrompt(question, desc, f, done, back).start()
}

func newPrompt(question, desc string, f *field, done, back func() (screen, tea.Cmd)) *prompt {
	return &prompt{question: question, desc: desc, f: f, done: done, back: back}
}

// key adds an action on a key, shown in the help line as hint.
func (p *prompt) key(k, hint string, fn func(*prompt) (screen, tea.Cmd)) *prompt {
	if p.extra == nil {
		p.extra = map[string]func(*prompt) (screen, tea.Cmd){}
	}
	p.extra[k] = fn
	p.hints = append(p.hints, hint)
	return p
}

func (p *prompt) start() (screen, tea.Cmd) {
	if p.f.kind == fText {
		return p, p.f.in.Focus()
	}
	return p, nil
}

func (p *prompt) update(msg tea.Msg) (screen, tea.Cmd) {
	f := p.f
	if k, ok := msg.(tea.KeyPressMsg); ok {
		p.err = ""
		switch k.String() {
		case "esc":
			return p.back()
		case "enter":
			if f.required && f.val() == "" {
				p.err = "required"
				return p, nil
			}
			return p.done()
		}
		if f.kind != fText {
			if fn, ok := p.extra[k.String()]; ok {
				return fn(p)
			}
			switch k.String() {
			case "up", "k":
				f.cur = max(f.cur-1, 0)
			case "down", "j":
				f.cur = min(f.cur+1, len(f.opts)-1)
			}
			return p, nil
		}
	}
	if f.kind == fText {
		if k, ok := msg.(tea.KeyPressMsg); ok && f.fresh {
			if f.fresh = false; k.Text != "" {
				f.in.SetValue("")
			}
		}
		var cmd tea.Cmd
		f.in, cmd = f.in.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p *prompt) view() string {
	lines := []string{p.question}
	if p.desc != "" {
		lines = append(lines, muted.Width(width).Render(p.desc))
	}
	lines = append(lines, "")
	switch p.f.kind {
	case fText:
		lines = append(lines, accent.Render("❯ ")+p.f.in.View())
	default:
		for j, o := range p.f.opts {
			if p.f.gaps[j] && j > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, option(o, j == p.f.cur))
		}
	}
	if p.err != "" {
		lines = append(lines, "", bad.Render(p.err))
	}
	return strings.Join(lines, "\n")
}

func (p *prompt) keys() []string {
	k := []string{"Enter to confirm"}
	if p.f.in.ShowSuggestions {
		k = append(k, "Tab to complete")
	}
	k = append(k, p.hints...)
	return append(k, "Esc to go back")
}
