package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func testProject(t *testing.T) *Project {
	t.Helper()
	p, err := initProject(t.TempDir(), Config{Region: "eu-central-1", From: "News <n@example.com>", List: "list", Template: "Simple"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Hello, World!":         "hello-world",
		"  --Über cool 42 --":   "ber-cool-42",
		"":                      "campaign",
		strings.Repeat("a", 60): strings.Repeat("a", 40),
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewCampaignAndRender(t *testing.T) {
	p := testProject(t)
	c, err := p.newCampaign("Hello <World>", "Simple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Slug, time.Now().Format("2006-01-02")+"-hello-world") {
		t.Errorf("slug %q", c.Slug)
	}
	m, err := p.render(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<h1>Hello &lt;World&gt;</h1>", "{{amazonSESUnsubscribeUrl}}", "<!doctype html>"} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("rendered html lacks %q", want)
		}
	}
	if strings.Contains(m.HTML, contentMarker) {
		t.Error("content marker left in output")
	}
	if m.Subject != "Hello <World>" || m.Text != "" {
		t.Errorf("mail = %+v", m)
	}

	// Same subject on the same day gets a numbered directory; no template means a full page.
	c2, err := p.newCampaign("Hello <World>", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Slug != c.Slug+"-2" {
		t.Errorf("second slug %q", c2.Slug)
	}
	m2, err := p.render(c2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m2.HTML, "<title>Hello &lt;World&gt;</title>") {
		t.Error("campaign without template is not a full page")
	}
	if err := os.WriteFile(p.campaignFile(c2.Slug, "body.txt"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m2, _ = p.render(c2); m2.Text != "plain" {
		t.Errorf("text part %q", m2.Text)
	}

	cs, err := p.campaigns()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Slug != c2.Slug || cs[0].status() != "draft" {
		t.Errorf("campaigns = %+v", cs)
	}
}

func TestCampaignRoundTrip(t *testing.T) {
	p := testProject(t)
	c, err := p.newCampaign("x", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	c.SentAt, c.Sent, c.Failed = &now, 10, 1
	if err := p.saveCampaign(c); err != nil {
		t.Fatal(err)
	}
	got, err := p.campaign(c.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.status() != "partial" || !got.SentAt.Equal(now) || got.Sent != 10 || got.Subject != "x" {
		t.Errorf("got %+v", got)
	}
}

func TestRenderMissingMarker(t *testing.T) {
	p := testProject(t)
	if err := os.WriteFile(p.path("templates", "bare.html"), []byte("<p>no marker</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := p.newCampaign("x", "bare")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.render(c); err == nil {
		t.Error("expected an error for a template without the content marker")
	}
}
