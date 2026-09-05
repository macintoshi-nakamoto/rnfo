package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/registry"
)

// health answers the only question that matters for a year-long unattended
// system: is every probe still writing? It looks at the newest run record on
// each active probe and calls the probe stale when its last controls run is
// older than the stale window (three quarter-hour slots by default).
//
// The daily pull runs it after collecting. With RNFO_ALERT_URL set in .env
// it also sends a one-line message when something is stale, so a dead probe
// is noticed the same day rather than at the next manual look.
//
//	rnfo-collect health [-stale 45m] [-alert]
func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	stale := fs.Duration("stale", 45*time.Minute, "how old the newest controls run may be before a probe counts as stale")
	alert := fs.Bool("alert", false, "send RNFO_ALERT_URL a message if anything is stale")
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := loadEnv(".env")
	if err != nil {
		return fmt.Errorf("read .env: %w", err)
	}
	reg, err := registry.Load("probes.yaml")
	if err != nil {
		return err
	}
	key := env["RNFO_SSH_KEY"]

	type status struct {
		probe   string
		ok      bool
		detail  string
		lastAge time.Duration
	}
	var out []status
	now := time.Now().UTC()
	for _, p := range reg.Active() {
		if p.Role == "responder" {
			continue
		}
		host := env["RNFO_SSH_"+strings.ReplaceAll(p.ID, "-", "_")]
		if host == "" {
			out = append(out, status{p.ID, false, "no RNFO_SSH_ entry in .env", 0})
			continue
		}
		dir := remoteDataDir(key, host)
		// Newest controls run record: yesterday's file first, then today's, so
		// that the last line of the concatenation is the most recent run.
		cmd := fmt.Sprintf(`for f in %s/runs/$(date -u -d yesterday +%%F 2>/dev/null || date -u -v-1d +%%F).jsonl %s/runs/$(date -u +%%F).jsonl; do [ -f "$f" ] && grep '"profile":"controls"' "$f"; done | grep '"kind":"run"' | tail -1`, dir, dir)
		raw, err := sshOut(key, host, cmd)
		if err != nil {
			out = append(out, status{p.ID, false, "unreachable: " + strings.TrimSpace(err.Error()), 0})
			continue
		}
		var rec struct {
			RunID      string `json:"run_id"`
			FinishedAt string `json:"finished_at"`
			Healthy    bool   `json:"healthy"`
			OK, Rows   int
			ASN        string `json:"asn"`
		}
		line := strings.TrimSpace(raw)
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			out = append(out, status{p.ID, false, "no controls run record found", 0})
			continue
		}
		t, err := time.Parse("2006-01-02T15:04:05.000Z", rec.FinishedAt)
		if err != nil {
			out = append(out, status{p.ID, false, "unparseable finished_at " + rec.FinishedAt, 0})
			continue
		}
		age := now.Sub(t)
		good := age <= *stale && rec.Healthy
		detail := fmt.Sprintf("last controls %s, %s ago, healthy=%v, asn=%s", rec.RunID, age.Round(time.Minute), rec.Healthy, rec.ASN)
		out = append(out, status{p.ID, good, detail, age})
	}

	allOK := true
	var lines []string
	for _, s := range out {
		mark := "OK   "
		if !s.ok {
			mark = "STALE"
			allOK = false
		}
		l := fmt.Sprintf("%s %-14s %s", mark, s.probe, s.detail)
		lines = append(lines, l)
		fmt.Println(l)
	}
	if !allOK && *alert {
		if u := env["RNFO_ALERT_URL"]; u != "" {
			msg := "RNFO: probe(s) stale\n" + strings.Join(lines, "\n")
			if err := notify(u, msg); err != nil {
				fmt.Println("alert failed:", err)
			} else {
				fmt.Println("alert sent")
			}
		}
	}
	if !allOK {
		os.Exit(1)
	}
	return nil
}

// notify delivers a message to RNFO_ALERT_URL. If the URL contains {text} it
// is substituted and fetched with GET, which is what a Telegram bot's
// sendMessage endpoint wants; otherwise the message is POSTed as JSON
// {"text": ...}, which most webhook receivers accept.
func notify(target, msg string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	if strings.Contains(target, "{text}") {
		u := strings.ReplaceAll(target, "{text}", url.QueryEscape(msg))
		resp, err := client.Get(u)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}
	body, _ := json.Marshal(map[string]string{"text": msg})
	resp, err := client.Post(target, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
