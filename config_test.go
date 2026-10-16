package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitAndOpenProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "news")
	cfg := Config{Profile: "p", Region: "eu-central-1", From: "a@b", List: "l", Topic: "t", Template: "Simple"}
	if _, err := initProject(dir, cfg); err != nil {
		t.Fatal(err)
	}
	p, err := openProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Config != cfg {
		t.Errorf("config = %+v", p.Config)
	}
	if p.listTopic() != "l / t" || p.consoleURL("account") != "https://eu-central-1.console.aws.amazon.com/ses/home?region=eu-central-1#/account" {
		t.Errorf("listTopic %q, consoleURL %q", p.listTopic(), p.consoleURL("account"))
	}

	// Initializing again keeps the user's template and does not duplicate the ignore line.
	if err := os.WriteFile(p.path("templates", "Simple.html"), []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initProject(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if gi, _ := os.ReadFile(p.path(".gitignore")); string(gi) != ".goletter/\n" {
		t.Errorf(".gitignore = %q", gi)
	}
	if tpl, _ := os.ReadFile(p.path("templates", "Simple.html")); string(tpl) != "custom" {
		t.Errorf("template overwritten: %q", tpl)
	}
	if _, err := openProject(t.TempDir()); !errors.Is(err, errNotInitialized) {
		t.Errorf("open empty dir: %v", err)
	}
}
