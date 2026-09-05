// Command rnfo-collect validates and gathers the data probes produce.
//
// Collection is pull-based on purpose. Probes hold no credentials and cannot
// reach the archive, so a probe that is compromised or seized cannot rewrite
// history. The collector reaches in, verifies the checksum the probe sealed the
// file with, and only then admits the day into the archive.
//
//	rnfo-collect validate [path...]   check files against the schema and registry
//	rnfo-collect pull [-probe id]     fetch sealed day files from probes
//	rnfo-collect stats [path...]      summarise what the archive contains
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/classify"
	"github.com/macintoshi-nakamoto/rnfo/internal/registry"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "pull":
		err = cmdPull(os.Args[2:])
	case "stats":
		err = cmdStats(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "health":
		err = cmdHealth(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rnfo-collect <validate|pull|stats> [args]

  validate [path...]   verify every record against the schema and probes.yaml
  pull [-probe id] [-live]
                       fetch sealed day files from probes listed in .env;
                       -live also fetches today's unsealed files as provisional
  stats [path...]      count rows, verdicts and probe coverage
  compare -slot <run_id> -subject <probe> -control <probe> [path...]
                       join one slot target by target: what failed only from
                       the subject is what its network did
  health [-stale 45m] [-alert]
                       is every active probe still writing? exit 1 if not;
                       -alert sends RNFO_ALERT_URL a message`)
	os.Exit(2)
}

// ---------------------------------------------------------------- validate

func cmdValidate(args []string) error {
	reg, err := registry.Load("probes.yaml")
	if err != nil {
		return err
	}
	paths := args
	if len(paths) == 0 {
		paths = []string{"data"}
	}
	files, err := expand(paths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("no .jsonl files found; nothing to validate")
		return nil
	}
	var problems, rows int
	for _, f := range files {
		p, r, err := validateFile(f, reg)
		if err != nil {
			return err
		}
		problems += p
		rows += r
	}
	fmt.Printf("\n%d rows checked in %d file(s), %d problem(s)\n", rows, len(files), problems)
	if problems > 0 {
		os.Exit(1)
	}
	return nil
}

// validateFile checks one JSONL file. Every problem is reported with a line
// number: a dataset that says "something is wrong somewhere" is not auditable.
func validateFile(path string, reg *registry.Registry) (problems, rows int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	ln := 0
	report := func(msg string) {
		problems++
		if problems <= 20 {
			fmt.Printf("%s:%d: %s\n", path, ln, msg)
		} else if problems == 21 {
			fmt.Printf("%s: ... further problems suppressed\n", path)
		}
	}
	for sc.Scan() {
		ln++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		rows++
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			report("not valid JSON: " + err.Error())
			continue
		}
		if s, _ := m["schema"].(string); s != "v1" {
			report(fmt.Sprintf("unknown schema %q", m["schema"]))
			continue
		}
		probe, _ := m["probe"].(string)
		if probe == "" {
			report("missing probe")
		} else if !reg.Known(probe) {
			report(fmt.Sprintf("unknown probe id %q (not in probes.yaml)", probe))
		}
		switch kind, _ := m["kind"].(string); kind {
		case "run":
			validateRun(m, report)
		case "event":
			validateEvent(m, report)
		default:
			validateMeasurement(m, reg, probe, report)
		}
	}
	if err := sc.Err(); err != nil {
		return problems, rows, fmt.Errorf("%s: %w", path, err)
	}
	if problems == 0 {
		fmt.Printf("%-60s %6d rows  ok\n", path, rows)
	}
	return problems, rows, nil
}

func validateMeasurement(m map[string]any, reg *registry.Registry, probe string, report func(string)) {
	for _, k := range []string{"run_id", "ts", "net", "target", "url", "list", "stage", "verdict"} {
		if s, _ := m[k].(string); s == "" {
			report("missing required field " + k)
		}
	}
	if ts, _ := m["ts"].(string); ts != "" {
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", ts); err != nil {
			report("ts is not UTC with milliseconds: " + ts)
		}
	}
	verdict, _ := m["verdict"].(string)
	if verdict != "" && !classify.Valid(verdict) {
		report("unknown verdict " + verdict)
	}
	// curl_rc is derived, so a row where it disagrees with the verdict has
	// been edited or produced by a mismatched agent version.
	if verdict != "" && classify.Valid(verdict) {
		if rc, ok := m["curl_rc"].(float64); ok && int(rc) != classify.CurlRC(verdict) {
			report(fmt.Sprintf("curl_rc %d does not match verdict %s (expected %d)", int(rc), verdict, classify.CurlRC(verdict)))
		}
	}
	switch st, _ := m["stage"].(string); st {
	case "ok", "dns", "tcp", "tls", "request", "response":
	default:
		report("unknown stage " + st)
	}
	switch n, _ := m["net"].(string); n {
	case "hosting", "eyeball", "mobile":
	default:
		report("unknown net " + n)
	}
	if p := reg.Get(probe); p != nil && p.Net != "" {
		if n, _ := m["net"].(string); n != p.Net {
			report(fmt.Sprintf("net %q disagrees with probes.yaml (%q)", n, p.Net))
		}
	}
	// attempt means two different things depending on the profile, and the
	// schema says so: in a scheduled run it is 1 (first try) or 2 (the
	// confirmation retry); in the sni experiment it is the round number, and
	// the experiment is run with as many rounds as the operator chooses.
	maxAttempt := 2.0
	if p, _ := m["profile"].(string); p == "sni" {
		maxAttempt = 100
	}
	if a, ok := m["attempt"].(float64); ok && (a < 1 || a > maxAttempt) {
		report(fmt.Sprintf("attempt out of range for profile %v: %v", m["profile"], a))
	}
	// The ethics rule is enforced here, not left to discipline: a probe
	// address must never appear in the dataset.
	if _, present := m["probe_ip"]; present {
		report("row contains probe_ip; probe addresses must never be stored")
	}
}

func validateRun(m map[string]any, report func(string)) {
	for _, k := range []string{"run_id", "started_at", "finished_at", "profile"} {
		if s, _ := m[k].(string); s == "" {
			report("run record missing " + k)
		}
	}
	if _, ok := m["healthy"].(bool); !ok {
		report("run record missing healthy")
	}
}

func validateEvent(m map[string]any, report func(string)) {
	if s, _ := m["type"].(string); s == "" {
		report("event record missing type")
	}
	if s, _ := m["ts"].(string); s == "" {
		report("event record missing ts")
	}
}

// ------------------------------------------------------------------- pull

func cmdPull(args []string) error {
	only := ""
	live := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-probe":
			if i+1 < len(args) {
				only = args[i+1]
			}
		case "-live":
			live = true
		}
	}
	env, err := loadEnv(".env")
	if err != nil {
		return fmt.Errorf("read .env (copy .env.example first): %w", err)
	}
	reg, err := registry.Load("probes.yaml")
	if err != nil {
		return err
	}
	archive := env["RNFO_ARCHIVE"]
	if archive == "" {
		archive = "data"
	}
	key := env["RNFO_SSH_KEY"]

	pulled, skipped, bad := 0, 0, 0
	for _, p := range reg.Probes {
		if p.Status != "active" || (only != "" && p.ID != only) {
			continue
		}
		host := env["RNFO_SSH_"+strings.ReplaceAll(p.ID, "-", "_")]
		if host == "" {
			fmt.Printf("%-16s no RNFO_SSH_ entry in .env, skipped\n", p.ID)
			continue
		}
		fmt.Printf("==> %s (%s)\n", p.ID, host)
		dataDir := remoteDataDir(key, host)
		for _, stream := range []string{"measurements", "runs"} {
			remote := dataDir + "/" + stream
			out, err := sshOut(key, host, "ls -1 "+remote+"/*.jsonl.sha256 2>/dev/null || true")
			if err != nil {
				fmt.Printf("    %s: %v\n", stream, err)
				continue
			}
			for _, sidecar := range strings.Fields(out) {
				name := strings.TrimSuffix(filepath.Base(sidecar), ".sha256")
				dst := filepath.Join(archive, p.ID, stream, name)
				if _, err := os.Stat(dst); err == nil {
					skipped++
					continue
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				want, err := sshOut(key, host, "cat "+sidecar)
				if err != nil {
					fmt.Printf("    %s: %v\n", name, err)
					continue
				}
				wantSum := strings.Fields(want)
				if len(wantSum) == 0 {
					fmt.Printf("    %s: empty checksum sidecar\n", name)
					continue
				}
				if err := scp(key, host, remote+"/"+name, dst); err != nil {
					fmt.Printf("    %s: %v\n", name, err)
					continue
				}
				got, err := fileSum(dst)
				if err != nil || got != wantSum[0] {
					// A day whose checksum does not match is not admitted. It
					// stays on the probe and is reported, because silently
					// accepting it would put unverifiable rows in the archive.
					_ = os.Remove(dst)
					fmt.Printf("    %s: CHECKSUM MISMATCH, rejected (probe %s, want %s)\n", name, wantSum[0][:12], got[:12])
					bad++
					continue
				}
				if err := os.WriteFile(dst+".sha256", []byte(want), 0o644); err != nil {
					return err
				}
				fmt.Printf("    %s/%s  verified\n", stream, name)
				pulled++
			}
			if live {
				// Today's file is still being written and has no checksum. It
				// is fetched for analysis only, named so it cannot be mistaken
				// for an archived day, and overwritten on every pull. When the
				// day is sealed the verified copy replaces it.
				today, err := sshOut(key, host, "date -u +%F")
				if err != nil {
					continue
				}
				name := strings.TrimSpace(today) + ".jsonl"
				dst := filepath.Join(archive, p.ID, stream, "live-"+name)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				if err := scp(key, host, remote+"/"+name, dst); err == nil {
					fmt.Printf("    %s/live-%s  provisional\n", stream, name)
				}
			}
		}
	}
	fmt.Printf("\npulled %d, already present %d, rejected %d\n", pulled, skipped, bad)
	if bad > 0 {
		os.Exit(1)
	}
	return nil
}

// remoteDataDir asks the probe where it keeps its data. Servers use
// /var/lib/rnfo/data; a handset under Termux has no /var and keeps it under
// $HOME/rnfo/data. The probe's own probe.env is the authority, wherever it is.
func remoteDataDir(key, host string) string {
	out, err := sshOut(key, host,
		`for f in /etc/rnfo/probe.env "$HOME/rnfo/probe.env"; do [ -f "$f" ] && grep -m1 '^RNFO_DATA_DIR=' "$f"; done | head -1 | cut -d= -f2`)
	if err == nil {
		if d := strings.TrimSpace(out); d != "" {
			return d
		}
	}
	return "/var/lib/rnfo/data"
}

func sshArgs(key, host string) []string {
	a := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=20"}
	if key != "" {
		a = append(a, "-i", expandHome(key))
	}
	return append(a, host)
}

func sshOut(key, host, cmd string) (string, error) {
	c := exec.Command("ssh", append(sshArgs(key, host), cmd)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("ssh: %s", strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func scp(key, host, remote, dst string) error {
	a := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if key != "" {
		a = append(a, "-i", expandHome(key))
	}
	a = append(a, host+":"+remote, dst)
	c := exec.Command("scp", a...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("scp: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ------------------------------------------------------------------ stats

func cmdStats(args []string) error {
	paths := args
	if len(paths) == 0 {
		paths = []string{"data"}
	}
	files, err := expand(paths)
	if err != nil {
		return err
	}
	type acc struct {
		rows     int
		verdicts map[string]int
		days     map[string]bool
	}
	byProbe := map[string]*acc{}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			if _, isRun := m["kind"]; isRun {
				continue
			}
			p, _ := m["probe"].(string)
			a := byProbe[p]
			if a == nil {
				a = &acc{verdicts: map[string]int{}, days: map[string]bool{}}
				byProbe[p] = a
			}
			a.rows++
			v, _ := m["verdict"].(string)
			a.verdicts[v]++
			if ts, _ := m["ts"].(string); len(ts) >= 10 {
				a.days[ts[:10]] = true
			}
		}
		fh.Close()
	}
	names := make([]string, 0, len(byProbe))
	for k := range byProbe {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		a := byProbe[n]
		fmt.Printf("\n%s: %d rows over %d day(s)\n", n, a.rows, len(a.days))
		type kv struct {
			k string
			v int
		}
		var vs []kv
		for k, v := range a.verdicts {
			vs = append(vs, kv{k, v})
		}
		sort.Slice(vs, func(i, j int) bool { return vs[i].v > vs[j].v })
		for _, e := range vs {
			fmt.Printf("  %-22s %6d  %5.1f%%\n", e.k, e.v, 100*float64(e.v)/float64(a.rows))
		}
	}
	return nil
}

// ----------------------------------------------------------------- helpers

func expand(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !fi.IsDir() {
			out = append(out, p)
			continue
		}
		err = filepath.Walk(p, func(q string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && filepath.Ext(q) == ".jsonl" {
				out = append(out, q)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func loadEnv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return m, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

func fileSum(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
