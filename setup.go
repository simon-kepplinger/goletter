package main

import (
	"cmp"
	"context"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// newConfirmInit asks before turning dir into a goletter project.
func newConfirmInit(a *app, dir string) (screen, tea.Cmd) {
	var s screen
	s = &confirm{
		text:  "Initialize goletter in " + tilde(dir) + "?\n" + muted.Render("This writes files into that directory."),
		yes:   "Yes, create",
		no:    "No, exit",
		onYes: func() (screen, tea.Cmd) { return newSetupForm(a, dir) },
		onNo:  func() (screen, tea.Cmd) { return s, tea.Quit },
	}
	return s, nil
}

// newSetupForm asks for the region, then checks the credentials. The AWS
// profile is "goletter" and can be changed in goletter.toml.
func newSetupForm(a *app, dir string) (screen, tea.Cmd) {
	cfg := Config{
		Profile:  "goletter",
		Region:   cmp.Or(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"), "eu-central-1"),
		Template: "Simple",
	}
	if a.p != nil { // back from the credential check: keep what was typed
		cfg = a.p.Config
	}
	region := text("").def(cfg.Region).must().suggest(sesRegions)
	return ask("Which AWS region do you send email from?", "The region where you verified your sender address in Amazon SES. Start typing for suggestions.", region, func() (screen, tea.Cmd) {
		cfg.Region = region.val()
		a.p, a.ses = &Project{Dir: dir, Config: cfg}, nil
		return newAccount(a, func() (screen, tea.Cmd) { return newSenderForm(a) })
	}, func() (screen, tea.Cmd) {
		a.p = nil
		return newConfirmInit(a, dir)
	})
}

// sesRegions is where SES is offered, for completing the region prompt.
var sesRegions = []string{
	"af-south-1",
	"ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
	"ap-south-1", "ap-south-2",
	"ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-5",
	"ca-central-1", "ca-west-1",
	"eu-central-1", "eu-central-2",
	"eu-north-1",
	"eu-south-1",
	"eu-west-1", "eu-west-2", "eu-west-3",
	"il-central-1",
	"me-central-1", "me-south-1",
	"sa-east-1",
	"us-east-1", "us-east-2",
	"us-gov-east-1", "us-gov-west-1",
	"us-west-1", "us-west-2",
}

// newSenderForm asks for sender, list and topic, then writes the project.
func newSenderForm(a *app) (screen, tea.Cmd) {
	type options struct {
		identities, lists []string
		topics            map[string][]string
	}
	return load(a, "Loading SES identities and contact lists…", func(ctx context.Context) (options, error) {
		o := options{topics: map[string][]string{}}
		s, err := a.client()
		if err != nil {
			return o, err
		}
		if o.identities, err = s.identities(ctx); err != nil {
			return o, err
		}
		if o.lists, err = s.lists(ctx); err != nil {
			return o, err
		}
		for _, l := range o.lists {
			if o.topics[l], err = s.topics(ctx, l); err != nil {
				return o, err
			}
		}
		return o, nil
	}, func(o options, err error) (screen, tea.Cmd) {
		back := func() (screen, tea.Cmd) {
			return newAccount(a, func() (screen, tea.Cmd) { return newSenderForm(a) })
		}
		if err != nil {
			return &message{err: err, next: back}, nil
		}
		cfg := &a.p.Config
		var askFrom, askList, askTopic func() (screen, tea.Cmd)
		askFrom = func() (screen, tea.Cmd) {
			desc := "Shown as the sender in every newsletter, e.g. News <news@example.com>. The address or its domain must be verified in SES."
			if len(o.identities) > 0 {
				desc += " Verified so far: " + strings.Join(o.identities, ", ")
			}
			from := text(cfg.From).must()
			return ask("Which address should the newsletter be sent from?", desc, from, func() (screen, tea.Cmd) {
				cfg.From = from.val()
				return askList()
			}, back)
		}
		// SES allows one contact list per account and region, so an existing
		// one is simply used and a new one only needs a name.
		askList = func() (screen, tea.Cmd) {
			if len(o.lists) > 0 {
				cfg.List = o.lists[0]
				return askTopic()
			}
			list := text("").def(cmp.Or(cfg.List, "newsletter")).must()
			return ask("What should the subscriber list be called?", "Your account has no subscriber list in SES yet, so goletter creates one. The name is only used internally.", list, func() (screen, tea.Cmd) {
				cfg.List, cfg.Topic = list.val(), ""
				return writeProject(a, o.lists)
			}, askFrom)
		}
		askTopic = func() (screen, tea.Cmd) {
			topics := o.topics[cfg.List]
			if len(topics) == 0 {
				cfg.Topic = ""
				return writeProject(a, o.lists)
			}
			topic := choice(append([]string{"whole list"}, topics...), cmp.Or(cfg.Topic, "whole list"))
			return ask("Which topic is this newsletter about?", "Your subscriber list has topics that people subscribe to individually. Only subscribers of the chosen topic receive the newsletter.", topic, func() (screen, tea.Cmd) {
				if cfg.Topic = topic.val(); cfg.Topic == "whole list" {
					cfg.Topic = ""
				}
				return writeProject(a, o.lists)
			}, askFrom)
		}
		return askFrom()
	})
}

// writeProject creates the contact list if needed and writes the project files.
func writeProject(a *app, lists []string) (screen, tea.Cmd) {
	cfg := a.p.Config
	return load(a, "Writing project…", func(ctx context.Context) (*Project, error) {
		if !slices.Contains(lists, cfg.List) {
			s, err := a.client()
			if err != nil {
				return nil, err
			}
			if err := s.createList(ctx, cfg.List); err != nil {
				return nil, err
			}
		}
		return initProject(a.p.Dir, cfg)
	}, func(p *Project, err error) (screen, tea.Cmd) {
		if err != nil {
			return &message{err: err, next: func() (screen, tea.Cmd) { return newSenderForm(a) }}, nil
		}
		a.p, a.ses = p, nil
		a.notify("project initialized in "+tilde(p.Dir), nil)
		return newMenu(a), nil
	})
}

// tilde shortens a path under the home directory.
func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
