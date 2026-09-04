package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// compare is the analysis the whole design exists for: one slot, one subject
// probe, one control probe, joined target by target.
//
// A target that failed from the subject and succeeded from the control is the
// only kind of failure that can be attributed to the subject's network. A
// target that failed from both is a dead site, a rotten list entry, or a host
// that dislikes datacentres - none of which says anything about filtering. A
// target that failed only from the control is a warning about the control
// itself: its address may be geoblocked or blocklisted, and if that happens a
// lot the control is not clean enough to be one.
//
//	rnfo-collect compare -slot 2026-09-05T00:00Z/full -subject ru-msk-vps -control nl-lim-panel [paths...]
func cmdCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	slot := fs.String("slot", "", "run id to compare, e.g. 2026-09-05T00:00Z/full")
	subject := fs.String("subject", "", "probe whose network is under study")
	control := fs.String("control", "", "probe outside that network")
	top := fs.Int("top", 25, "how many subject-only failures to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *slot == "" || *subject == "" || *control == "" {
		return fmt.Errorf("compare needs -slot, -subject and -control")
	}
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{"data"}
	}
	files, err := expand(paths)
	if err != nil {
		return err
	}

	// outcome is the final word on one target from one probe: ok if any
	// attempt succeeded, otherwise the verdict of the last attempt. Keeping
	// the first-attempt verdict as well lets the report say how much of the
	// failure was transient.
	type outcome struct {
		ok       bool
		verdict  string // final
		first    string // attempt 1
		category string
		list     string
		stage    string
		bytes    int64
		attempts int
	}
	sub := map[string]*outcome{}
	ctl := map[string]*outcome{}

	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			var m struct {
				RunID    string `json:"run_id"`
				Kind     string `json:"kind"`
				Probe    string `json:"probe"`
				URL      string `json:"url"`
				Verdict  string `json:"verdict"`
				Category string `json:"category"`
				List     string `json:"list"`
				Stage    string `json:"stage"`
				Attempt  int    `json:"attempt"`
				Bytes    int64  `json:"bytes_read"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.Kind != "" || m.RunID != *slot {
				continue
			}
			var dst map[string]*outcome
			switch m.Probe {
			case *subject:
				dst = sub
			case *control:
				dst = ctl
			default:
				continue
			}
			o := dst[m.URL]
			if o == nil {
				o = &outcome{category: m.Category, list: m.List}
				dst[m.URL] = o
			}
			o.attempts++
			if m.Attempt == 1 {
				o.first = m.Verdict
			}
			if m.Verdict == "ok" {
				o.ok = true
			}
			// Later attempts overwrite: the final verdict is the last one.
			if !o.ok {
				o.verdict, o.stage, o.bytes = m.Verdict, m.Stage, m.Bytes
			} else {
				o.verdict = "ok"
			}
		}
		fh.Close()
	}
	if len(sub) == 0 || len(ctl) == 0 {
		return fmt.Errorf("slot %s: subject has %d targets, control has %d; nothing to join", *slot, len(sub), len(ctl))
	}

	type row struct {
		url string
		s   *outcome
		c   *outcome
	}
	var okBoth, subOnly, ctlOnly, failBoth []row
	unpaired := 0
	for u, s := range sub {
		c := ctl[u]
		if c == nil {
			unpaired++
			continue
		}
		r := row{u, s, c}
		switch {
		case s.ok && c.ok:
			okBoth = append(okBoth, r)
		case !s.ok && c.ok:
			subOnly = append(subOnly, r)
		case s.ok && !c.ok:
			ctlOnly = append(ctlOnly, r)
		default:
			failBoth = append(failBoth, r)
		}
	}
	for u := range ctl {
		if sub[u] == nil {
			unpaired++
		}
	}
	paired := len(okBoth) + len(subOnly) + len(ctlOnly) + len(failBoth)
	pct := func(n int) string { return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(paired)) }

	fmt.Printf("slot %s\n  subject %s   control %s\n  %d paired targets, %d unpaired\n\n",
		*slot, *subject, *control, paired, unpaired)
	fmt.Printf("  %-34s %6d  %s\n", "ok from both", len(okBoth), pct(len(okBoth)))
	fmt.Printf("  %-34s %6d  %s   <- attributable to the subject's network\n", "failed from subject only", len(subOnly), pct(len(subOnly)))
	fmt.Printf("  %-34s %6d  %s   <- site down, list rot, or dislikes datacentres\n", "failed from both", len(failBoth), pct(len(failBoth)))
	fmt.Printf("  %-34s %6d  %s   <- a warning about the control's own address\n", "failed from control only", len(ctlOnly), pct(len(ctlOnly)))

	hist := func(title string, rows []row, pick func(row) string) {
		if len(rows) == 0 {
			return
		}
		h := map[string]int{}
		for _, r := range rows {
			h[pick(r)]++
		}
		type kv struct {
			k string
			v int
		}
		var kvs []kv
		for k, v := range h {
			kvs = append(kvs, kv{k, v})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v || (kvs[i].v == kvs[j].v && kvs[i].k < kvs[j].k) })
		fmt.Printf("\n  %s\n", title)
		for _, e := range kvs {
			fmt.Printf("    %-26s %5d\n", e.k, e.v)
		}
	}

	hist("subject-only failures, by mechanism (final verdict):", subOnly, func(r row) string { return r.s.verdict })
	hist("subject-only failures, by list:", subOnly, func(r row) string { return r.s.list })
	hist("subject-only failures, by category:", subOnly, func(r row) string { return r.s.category })

	// Transience: of the subject-only failures, how many failed on the first
	// attempt and also on the retry? That is the share that is not noise.
	if len(subOnly) > 0 {
		reproduced := 0
		for _, r := range subOnly {
			if r.s.attempts >= 2 {
				reproduced++
			}
		}
		fmt.Printf("\n  of %d subject-only failures, %d were retried and still failed (%.1f%% reproduced)\n",
			len(subOnly), reproduced, 100*float64(reproduced)/float64(len(subOnly)))
	}

	hist("control-only failures, by mechanism:", ctlOnly, func(r row) string { return r.c.verdict })

	sort.Slice(subOnly, func(i, j int) bool {
		if subOnly[i].s.verdict != subOnly[j].s.verdict {
			return subOnly[i].s.verdict < subOnly[j].s.verdict
		}
		return subOnly[i].url < subOnly[j].url
	})
	if n := *top; n > 0 && len(subOnly) > 0 {
		if n > len(subOnly) {
			n = len(subOnly)
		}
		fmt.Printf("\n  first %d subject-only failures:\n", n)
		for _, r := range subOnly[:n] {
			fmt.Printf("    %-20s %-6s %6dB  %s\n", r.s.verdict, r.s.category, r.s.bytes, trimURL(r.url, 60))
		}
	}
	return nil
}

func trimURL(u string, n int) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if len(u) > n {
		return u[:n-1] + "…"
	}
	return u
}
