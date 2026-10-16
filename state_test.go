package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSendLog(t *testing.T) {
	p := testProject(t)
	if l, err := p.readLog("none"); err != nil || len(l) != 0 {
		t.Errorf("missing log: %v %v", l, err)
	}
	recs := []sendRecord{{Email: "a@x", Status: statusSuccess}, {Email: "b@x", Status: "MessageRejected", Error: "nope"}}
	if err := p.appendLog("c", recs); err != nil {
		t.Fatal(err)
	}
	if err := p.appendLog("c", []sendRecord{{Email: "b@x", Status: statusSuccess}}); err != nil {
		t.Fatal(err)
	}
	log, err := p.readLog("c")
	if err != nil {
		t.Fatal(err)
	}
	ka, _ := p.pseudonym("a@x")
	kb, _ := p.pseudonym("B@x ")
	if len(log) != 2 || log[ka].Status != statusSuccess || log[kb].Status != statusSuccess {
		t.Errorf("log = %+v", log)
	}
	if data, _ := os.ReadFile(p.logPath("c")); strings.Contains(string(data), "@x") {
		t.Errorf("log on disk holds addresses: %s", data)
	}
	if err := p.appendLog("d", []sendRecord{{Email: "c@x", Status: "MessageRejected", Error: "c@x is not verified"}}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p.logPath("d")); !strings.Contains(string(data), "[recipient] is not verified") {
		t.Errorf("error not redacted: %s", data)
	}
}

func TestTestHistory(t *testing.T) {
	p := testProject(t)
	if h := p.testHistory(); len(h) != 0 {
		t.Errorf("empty history = %v", h)
	}
	for i := range 12 {
		for _, a := range []string{fmt.Sprintf("u%d@x", i), "me@x"} {
			if err := p.rememberTest(a); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := p.testHistory()
	if len(h) != 10 || h[0] != "me@x" || h[1] != "u11@x" || h[9] != "u3@x" {
		t.Errorf("history = %v", h)
	}
}
