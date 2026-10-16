package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Keys on the machine must never be picked up: only a login session counts.
func TestLoginCredentialsIgnoresOtherSources(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	os.WriteFile(cfg, []byte("[profile keys]\naws_access_key_id = AKIA\naws_secret_access_key = x\n\n[profile login]\nlogin_session = arn:aws:sts::1:assumed-role/r/s\n"), 0o600)
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "x")
	t.Setenv("AWS_LOGIN_CACHE_DIRECTORY", dir)

	for _, p := range []string{"", "keys", "missing"} {
		if _, err := loginConfig(context.Background(), Config{Profile: p}); !errors.Is(err, errNotLoggedIn) {
			t.Errorf("profile %q: got %v, want no login session", p, err)
		}
	}
	ac, err := loginConfig(context.Background(), Config{Profile: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ac.Credentials.Retrieve(context.Background()); err == nil || !strings.Contains(err.Error(), "token file not found") {
		t.Errorf("got %v, want missing token file", err)
	}
}

func TestConfigSetName(t *testing.T) {
	if got := configSet("2026-10-16-hello"); got != "goletter-2026-10-16-hello" {
		t.Errorf("got %q", got)
	}
	if got := configSet(strings.Repeat("x", 80)); len(got) != 64 || !strings.HasPrefix(got, "goletter-x") {
		t.Errorf("got %q (%d)", got, len(got))
	}
}
