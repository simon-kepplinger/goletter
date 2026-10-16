package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const contentMarker = "{{content}}"

// Campaign is campaigns/<slug>/campaign.toml. Subject and template are
// authored; the rest is written by goletter after sending.
type Campaign struct {
	Slug     string     `toml:"-"`
	Subject  string     `toml:"subject"`
	Template string     `toml:"template"`
	SentAt   *time.Time `toml:"sent_at,omitempty"`
	Sent     int        `toml:"sent,omitempty"`
	Failed   int        `toml:"failed,omitempty"`
}

const campaignHeader = `# goletter campaign. body.html is the content (a fragment when a template is
# set, else a full page), body.txt an optional plain-text part.
# sent_at, sent and failed are written by goletter.
`

// Mail is a campaign rendered for sending.
type Mail struct{ Subject, HTML, Text string }

func (c *Campaign) status() string {
	switch {
	case c.SentAt == nil:
		return "draft"
	case c.Failed > 0:
		return "partial"
	default:
		return "sent"
	}
}

// created is the day the campaign was made, from its folder name.
func (c *Campaign) created() time.Time {
	if t, err := time.Parse("2006-01-02", c.Slug[:min(10, len(c.Slug))]); err == nil {
		return t
	}
	if c.SentAt != nil {
		return *c.SentAt
	}
	return time.Now()
}

func (p *Project) campaignFile(slug, name string) string {
	return p.path("campaigns", slug, name)
}

// campaignCount counts the campaign folders, cheap enough for every redraw.
func (p *Project) campaignCount() int {
	entries, _ := os.ReadDir(p.path("campaigns"))
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// campaigns lists all campaigns, newest first.
func (p *Project) campaigns() ([]*Campaign, error) {
	entries, err := os.ReadDir(p.path("campaigns"))
	if err != nil {
		return nil, err
	}
	var cs []*Campaign
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := p.campaign(e.Name())
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	slices.Reverse(cs) // dirs are date-prefixed and ReadDir sorts ascending
	return cs, nil
}

func (p *Project) campaign(slug string) (*Campaign, error) {
	data, err := os.ReadFile(p.campaignFile(slug, "campaign.toml"))
	if err != nil {
		return nil, err
	}
	c := &Campaign{Slug: slug}
	if err := toml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", slug, err)
	}
	return c, nil
}

func (p *Project) saveCampaign(c *Campaign) error {
	return writeTOML(p.campaignFile(c.Slug, "campaign.toml"), campaignHeader, c)
}

// newCampaign scaffolds campaigns/<date>-<slug>/ with campaign.toml and body.html.
func (p *Project) newCampaign(subject, template string) (*Campaign, error) {
	base := time.Now().Format("2006-01-02") + "-" + slugify(subject)
	slug := base
	for i := 2; exists(p.campaignFile(slug, "")); i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.MkdirAll(p.campaignFile(slug, ""), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.campaignFile(slug, "body.html"), []byte(scaffold(subject, template)), 0o644); err != nil {
		return nil, err
	}
	c := &Campaign{Slug: slug, Subject: subject, Template: template}
	return c, p.saveCampaign(c)
}

// deleteCampaign removes the campaign folder and its local send log.
func (p *Project) deleteCampaign(c *Campaign) error {
	for _, path := range []string{p.campaignFile(c.Slug, ""), p.logPath(c.Slug), p.statsPath(c.Slug)} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// render merges body.html into the campaign's template at {{content}} and
// adds body.txt as the plain-text part when present.
func (p *Project) render(c *Campaign) (Mail, error) {
	body, err := os.ReadFile(p.campaignFile(c.Slug, "body.html"))
	if err != nil {
		return Mail{}, err
	}
	m := Mail{Subject: c.Subject}
	if m.HTML, err = p.wrap(c.Template, string(body)); err != nil {
		return Mail{}, err
	}
	if txt, err := os.ReadFile(p.campaignFile(c.Slug, "body.txt")); err == nil {
		m.Text = string(txt)
	}
	return m, nil
}

// wrap puts body into the template at {{content}}; with no template the
// body is the whole page already.
func (p *Project) wrap(template, body string) (string, error) {
	if template == "" {
		return body, nil
	}
	tpl, err := os.ReadFile(p.path("templates", template+".html"))
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(tpl), contentMarker) {
		return "", fmt.Errorf("template %s has no %s marker", template, contentMarker)
	}
	return strings.Replace(string(tpl), contentMarker, body, 1), nil
}

// scaffold is the starting body.html of a campaign: content only when a
// template provides the page, otherwise a complete page.
func scaffold(subject, template string) string {
	body := fmt.Sprintf(bodyScaffold, html.EscapeString(subject))
	if template == "" {
		body = fmt.Sprintf(pageScaffold, html.EscapeString(subject), body)
	}
	return body
}

// templates lists the names of templates/*.html.
func (p *Project) templates() []string {
	files, _ := filepath.Glob(p.path("templates", "*.html"))
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = strings.TrimSuffix(filepath.Base(f), ".html")
	}
	return names
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// slugify turns a subject into a short name safe for directories and SES tags.
func slugify(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	if s := strings.TrimRight(b.String(), "-"); s != "" {
		return s
	}
	return "campaign"
}

const bodyScaffold = `<h1>%s</h1>
<p>Write your newsletter here.</p>
`

const pageScaffold = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>%s</title>
</head>
<body>
%s
<p><a href="{{amazonSESUnsubscribeUrl}}">Unsubscribe</a></p>
</body>
</html>
`

const simpleTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  body { margin: 0; padding: 24px 16px; background: #ffffff; color: #212529; font-family: system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; font-size: 16px; line-height: 1.5; }
  main { max-width: 600px; margin: 0 auto; }
  h1, h2, h3 { line-height: 1.2; margin: 0 0 16px; }
  p { margin: 0 0 16px; }
  img { max-width: 100%; height: auto; }
  a { color: #0d6efd; }
  footer { max-width: 600px; margin: 32px auto 0; padding-top: 16px; border-top: 1px solid #dee2e6; font-size: 13px; color: #6c757d; }
</style>
</head>
<body>
  <main>
{{content}}
  </main>
  <footer>
    You receive this email because you subscribed.
    <a href="{{amazonSESUnsubscribeUrl}}">Unsubscribe</a>
  </footer>
</body>
</html>
`
