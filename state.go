package main

// Device-local state under .goletter/ (gitignored): send logs, the
// test-recipient history and cached statistics. Subscriber addresses never
// reach the disk: the send log stores a keyed hash of each address instead.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const statusSuccess = "SUCCESS"

// sendRecord is one line of .goletter/sent/<slug>.jsonl.
type sendRecord struct {
	Email     string    `json:"-"`   // only in memory
	Key       string    `json:"key"` // pseudonym of Email
	MessageID string    `json:"message_id,omitempty"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Time      time.Time `json:"time"`
}

// pseudonym stands in for an address in the send log: an HMAC with a random
// key kept in .goletter/key, so the log alone reveals no addresses.
func (p *Project) pseudonym(email string) (string, error) {
	if p.secret == nil {
		path := p.path(stateDir, "key")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			data = make([]byte, 32)
			rand.Read(data)
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.WriteFile(path, data, 0o600)
			}
		}
		if err != nil {
			return "", err
		}
		p.secret = data
	}
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(mac.Sum(nil))[:32], nil
}

func (p *Project) logPath(slug string) string {
	return p.path(stateDir, "sent", slug+".jsonl")
}

// appendLog adds records to the campaign's send log.
func (p *Project) appendLog(slug string, recs []sendRecord) error {
	f, err := appendFile(p.logPath(slug))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if r.Key, err = p.pseudonym(r.Email); err != nil {
			return err
		}
		r.Error = strings.ReplaceAll(r.Error, r.Email, "[recipient]") // SES quotes the address in some errors
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// readLog returns the latest record per recipient pseudonym.
func (p *Project) readLog(slug string) (map[string]sendRecord, error) {
	latest := map[string]sendRecord{}
	f, err := os.Open(p.logPath(slug))
	if errors.Is(err, os.ErrNotExist) {
		return latest, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var r sendRecord
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			return latest, nil
		} else if err != nil {
			return nil, err
		}
		latest[r.Key] = r
	}
}

func (p *Project) testHistoryPath() string {
	return p.path(stateDir, "test-recipients")
}

// testHistory returns the ten most recently used test addresses, newest first.
func (p *Project) testHistory() []string {
	data, _ := os.ReadFile(p.testHistoryPath())
	lines := strings.Fields(string(data))
	seen := map[string]bool{}
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < 10; i-- {
		if !seen[lines[i]] {
			seen[lines[i]] = true
			out = append(out, lines[i])
		}
	}
	return out
}

func (p *Project) rememberTest(addr string) error {
	f, err := appendFile(p.testHistoryPath())
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, addr)
	return err
}

func (p *Project) statsPath(slug string) string {
	return p.path(stateDir, "stats", slug+".json")
}

// saveStats keeps the last statistics seen, since SES forgets them after
// 60 days.
func (p *Project) saveStats(slug string, st Stats) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.statsPath(slug)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p.statsPath(slug), data, 0o600)
}

func (p *Project) cachedStats(slug string) (Stats, bool) {
	var st Stats
	data, err := os.ReadFile(p.statsPath(slug))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return Stats{}, false
	}
	return st, true
}

func appendFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}
