package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/registry"
)

// health answers the only question that matters for a year-long unattended
// system: is every probe still writing?
//
// One round trip per probe. The status command is fixed text, so a watcher
// can hold a key that is allowed to run exactly that command and nothing
// else (a forced-command key). The same text is what the workstation sends
// with its unrestricted key; the answer is the same either way.
//
// Alerts are about changes, not about state. A probe that goes stale is
// reported once, then again every -repeat while it stays stale, then once
// more when it recovers. Without that, an hourly check of a dead probe would
// be an hourly message for as long as nobody fixed it, and the messages would
// be ignored long before the probe was.
//
//	rnfo-collect health [-probes a,b] [-stale 45m] [-alert] [-repeat 6h]
//	                    [-state path] [-responder]
func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	only := fs.String("probes", "", "comma-separated probe ids to check; default: every active probe with an ssh entry")
	stale := fs.Duration("stale", 45*time.Minute, "how old the newest controls run may be before a probe counts as stale")
	alert := fs.Bool("alert", false, "send RNFO_ALERT_URL a message on state changes")
	repeat := fs.Duration("repeat", 6*time.Hour, "while stale, repeat the alert this often")
	statePath := fs.String("state", filepath.Join("data", "logs", "health.state.json"), "where the alert state lives")
	responder := fs.Bool("responder", false, "also check the responder answers with the registered certificate")
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

	want := map[string]bool{}
	for _, p := range strings.Split(*only, ",") {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}

	type row struct {
		name   string
		ok     bool
		detail string
	}
	var rows []row
	now := time.Now().UTC()

	for _, p := range reg.Active() {
		if len(want) > 0 && !want[p.ID] {
			continue
		}
		host := env["RNFO_SSH_"+strings.ReplaceAll(p.ID, "-", "_")]
		if host == "" {
			if len(want) > 0 {
				rows = append(rows, row{p.ID, false, "no RNFO_SSH_ entry in .env"})
			}
			continue // not this watcher's job
		}
		raw, err := statusOut(key, host)
		if err != nil {
			rows = append(rows, row{p.ID, false, "unreachable: " + firstLine(err.Error())})
			continue
		}
		var rec struct {
			RunID      string   `json:"run_id"`
			FinishedAt string   `json:"finished_at"`
			Healthy    bool     `json:"healthy"`
			ASN        string   `json:"asn"`
			ClockMS    *float64 `json:"clock_offset_ms"`
		}
		line := strings.TrimSpace(raw)
		if i := strings.LastIndex(line, "\n"); i >= 0 {
			line = line[i+1:]
		}
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			rows = append(rows, row{p.ID, false, "no controls run record found"})
			continue
		}
		t, err := time.Parse("2006-01-02T15:04:05.000Z", rec.FinishedAt)
		if err != nil {
			rows = append(rows, row{p.ID, false, "unparseable finished_at " + rec.FinishedAt})
			continue
		}
		age := now.Sub(t)
		good := age <= *stale && rec.Healthy
		clock := ""
		if rec.ClockMS != nil {
			clock = fmt.Sprintf(", clock %+.0fms", *rec.ClockMS)
			if *rec.ClockMS > 5000 || *rec.ClockMS < -5000 {
				good = false
				clock += " (too far off)"
			}
		}
		rows = append(rows, row{p.ID, good, fmt.Sprintf("last controls %s, %s ago, healthy=%v, %s%s",
			rec.RunID, age.Round(time.Minute), rec.Healthy, rec.ASN, clock)})
	}

	if *responder {
		if r, ok := checkResponder(env, reg); ok {
			rows = append(rows, r)
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	allOK := true
	var lines []string
	for _, r := range rows {
		mark := "OK   "
		if !r.ok {
			mark = "STALE"
			allOK = false
		}
		l := fmt.Sprintf("%s %-14s %s", mark, r.name, r.detail)
		lines = append(lines, l)
		fmt.Println(l)
	}

	if *alert {
		if u := env["RNFO_ALERT_URL"]; u != "" {
			st := loadHealthState(*statePath)
			var msgs []string
			for _, r := range rows {
				prev := st[r.name]
				switch {
				case !r.ok && (!prev.Stale || now.Sub(prev.LastAlert) >= *repeat):
					msgs = append(msgs, "PROBLEM "+r.name+": "+r.detail)
					st[r.name] = healthEntry{Stale: true, LastAlert: now}
				case !r.ok:
					// still stale, already reported recently
				case r.ok && prev.Stale:
					msgs = append(msgs, "RECOVERED "+r.name+": "+r.detail)
					st[r.name] = healthEntry{Stale: false, LastAlert: now}
				default:
					st[r.name] = healthEntry{Stale: false, LastAlert: prev.LastAlert}
				}
			}
			// The state advances only once the message is delivered. If the
			// alert endpoint is unreachable, the watcher itself has most likely
			// lost its network, and "probe unreachable" from a watcher without
			// a network is not a finding about the probe. Persisting it would
			// swallow the PROBLEM and then send a RECOVERED for an outage that
			// never happened, which is what the workstation did on 2026-09-09
			// when its uplink dropped for an hour. Left as it was, the next
			// run re-evaluates from the previous state.
			if len(msgs) == 0 {
				saveHealthState(*statePath, st)
			} else {
				host, _ := os.Hostname()
				msg := fmt.Sprintf("RNFO health from %s, %s\n%s", host, now.Format("2006-01-02 15:04 UTC"), strings.Join(msgs, "\n"))
				if err := notify(u, msg); err != nil {
					fmt.Println("alert failed:", err)
					fmt.Println("alert state not advanced: this watcher may be the one without a network")
				} else {
					saveHealthState(*statePath, st)
					fmt.Printf("alert sent (%d item(s))\n", len(msgs))
				}
			}
		}
	}
	if !allOK {
		os.Exit(1)
	}
	return nil
}

// statusCommand prints the newest controls run record of the probe it runs
// on. POSIX sh only, because it also has to run under Termux. It is the whole
// interface between a watcher and a probe, which is why it fits on a key.
const statusCommand = `D=$(for f in /etc/rnfo/probe.env "$HOME/rnfo/probe.env"; do [ -f "$f" ] && grep -m1 "^RNFO_DATA_DIR=" "$f"; done | head -1 | cut -d= -f2); [ -z "$D" ] && D=/var/lib/rnfo/data; Y=$(date -u -d yesterday +%F 2>/dev/null || date -u -v-1d +%F 2>/dev/null); T=$(date -u +%F); for f in "$D/runs/$Y.jsonl" "$D/runs/$T.jsonl"; do [ -f "$f" ] && grep '"profile":"controls"' "$f"; done | grep '"kind":"run"' | tail -1`

// statusOut runs the status command on a probe. The host "local" means this
// machine, for a watcher that is itself a probe.
func statusOut(key, host string) (string, error) {
	if host == "local" {
		out, err := exec.Command("sh", "-c", statusCommand).Output()
		return string(out), err
	}
	return sshOut(key, host, statusCommand)
}

// checkResponder fetches /v1/health from the responder and compares the
// certificate it presents with the fingerprint in the registry. A different
// fingerprint means we are not talking to our own service.
func checkResponder(env map[string]string, reg *registry.Registry) (r struct {
	name   string
	ok     bool
	detail string
}, present bool) {
	r.name = "responder"
	ip := env["RNFO_RESPONDER_IP"]
	if ip == "" {
		return r, false
	}
	want := ""
	for _, p := range reg.Probes {
		if p.ResponderCertSHA256 != "" {
			want = strings.ToLower(p.ResponderCertSHA256)
		}
	}
	got := ""
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // identity is checked by fingerprint below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) > 0 {
					s := sha256.Sum256(cs.PeerCertificates[0].Raw)
					got = hex.EncodeToString(s[:])
				}
				return nil
			},
		}},
	}
	resp, err := client.Get("https://" + ip + "/v1/health")
	if err != nil {
		r.detail = "unreachable: " + firstLine(err.Error())
		return r, true
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode != 200:
		r.detail = fmt.Sprintf("answered HTTP %d", resp.StatusCode)
	case want != "" && got != want:
		r.detail = fmt.Sprintf("certificate %s... is not the registered %s...", got[:12], want[:12])
	default:
		r.ok = true
		r.detail = fmt.Sprintf("answers, certificate %s... matches the registry", got[:12])
	}
	return r, true
}

type healthEntry struct {
	Stale     bool      `json:"stale"`
	LastAlert time.Time `json:"last_alert"`
}

func loadHealthState(path string) map[string]healthEntry {
	st := map[string]healthEntry{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveHealthState(path string, st map[string]healthEntry) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(path, b, 0o600)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
