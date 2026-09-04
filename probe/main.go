// Command rnfo-probe measures reachability from one vantage point and writes
// one JSONL row per target per run.
//
// Design rules it enforces:
//   - every target produces a row, including failures;
//   - a missing row means the probe was down, so every run also writes a run
//     record and downtime is reconstructible;
//   - the run id is derived from the scheduled slot, not from the start time,
//     so a Russian run and its foreign control share an id and can be paired;
//   - nothing outside the pinned lists and our own endpoints is ever measured.
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/classify"
	"github.com/macintoshi-nakamoto/rnfo/internal/identity"
	"github.com/macintoshi-nakamoto/rnfo/internal/jsonl"
	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
	"github.com/macintoshi-nakamoto/rnfo/internal/targets"
	"github.com/macintoshi-nakamoto/rnfo/probe/lists"
	"github.com/macintoshi-nakamoto/rnfo/probe/tests"
)

// slotPeriod is how often each profile is scheduled. The run id is the current
// time floored to this period, which is what makes ids line up across probes
// even when they start a few seconds apart.
var slotPeriod = map[string]time.Duration{
	"full":     6 * time.Hour,
	"controls": 15 * time.Minute,
}

// retryCeiling: if more than this share of targets failed, the second pass is
// skipped. When a probe has lost its uplink, retrying every target proves
// nothing and doubles the load on the target list for no information.
const retryCeiling = 0.40

func main() {
	var (
		probeID  = flag.String("probe", env("RNFO_PROBE_ID", ""), "probe id, must exist in probes.yaml")
		netType  = flag.String("net", env("RNFO_NET", ""), "network type: hosting, eyeball or mobile")
		profile  = flag.String("profile", "full", "target profile: full or controls")
		dataDir  = flag.String("data", env("RNFO_DATA_DIR", "/var/lib/rnfo/data"), "data directory")
		stateDir = flag.String("state", env("RNFO_STATE_DIR", "/var/lib/rnfo/state"), "state directory, never shipped")
		ownFile  = flag.String("own", env("RNFO_OWN_TARGETS", ""), "optional CSV of our own endpoints")
		exper    = flag.String("experiment", "", "run a one-off experiment instead of a scheduled measurement: sni")
		respIP   = flag.String("responder", env("RNFO_RESPONDER_IP", ""), "responder address for -experiment")
		respPort = flag.String("responder-port", env("RNFO_RESPONDER_PORT", "443"), "responder port for -experiment")
		repeats  = flag.Int("repeats", 5, "rounds per name in -experiment sni")
		family   = flag.String("family", env("RNFO_IP_FAMILY", "v4"), "address family to measure over: v4, v6 or auto")
		conc     = flag.Int("concurrency", envInt("RNFO_CONCURRENCY", 12), "parallel measurements")
		keepDays = flag.Int("keep-days", envInt("RNFO_KEEP_DAYS", 90), "days of local data to retain")
		runID    = flag.String("run-id", "", "override the slot-derived run id")
		dryRun   = flag.Bool("dry-run", false, "measure a handful of targets and print rows to stdout")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(schema.AgentVersion, "schema", schema.Version)
		return
	}
	if *probeID == "" || *netType == "" {
		log.Fatal("probe id and net type are required (-probe, -net or RNFO_PROBE_ID, RNFO_NET)")
	}
	switch *family {
	case "v4", "v6", "auto":
	default:
		log.Fatal("family must be v4, v6 or auto")
	}
	switch *netType {
	case schema.NetHosting, schema.NetEyeball, schema.NetMobile:
	default:
		log.Fatalf("net must be one of %s, %s, %s", schema.NetHosting, schema.NetEyeball, schema.NetMobile)
	}
	if _, ok := targets.Profiles[*profile]; !ok {
		log.Fatalf("unknown profile %q", *profile)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	started := time.Now().UTC()
	slot := *runID
	if slot == "" {
		slot = started.Truncate(slotPeriod[*profile]).Format("2006-01-02T15:04Z") + "/" + *profile
	}

	// Identity first: a run whose network we cannot name is still worth
	// having, but the ambiguity must be recorded rather than guessed at.
	id, prev, fresh := identity.Resolve(ctx, *stateDir)

	if *exper != "" {
		if *exper != "sni" {
			log.Fatalf("unknown experiment %q", *exper)
		}
		if *respIP == "" {
			log.Fatal("-responder <ip> is required for -experiment sni")
		}
		runSNI(ctx, sniArgs{
			probeID: *probeID, netType: *netType, family: *family,
			dataDir: *dataDir, ip: *respIP, port: *respPort,
			repeats: *repeats, started: started, runID: *runID,
			asn: id.ASN, country: id.Country, region: id.Region,
		})
		return
	}

	own, err := loadOwn(*ownFile)
	if err != nil {
		log.Fatalf("own targets: %v", err)
	}
	sub, err := fs.Sub(lists.FS, ".")
	if err != nil {
		log.Fatalf("lists: %v", err)
	}
	set, err := targets.Load(sub, *profile, own)
	if err != nil {
		log.Fatalf("targets: %v", err)
	}
	set.Order(slot)

	if *dryRun {
		runDry(ctx, set, 8, *family)
		return
	}

	mw, err := jsonl.New(*dataDir, "measurements")
	if err != nil {
		log.Fatalf("open measurements: %v", err)
	}
	defer mw.Close()
	rw, err := jsonl.New(*dataDir, "runs")
	if err != nil {
		log.Fatalf("open runs: %v", err)
	}
	defer rw.Close()

	writeEvent := func(typ, from, to, note string) {
		_ = rw.Write(schema.Event{
			Schema: schema.Version, Kind: "event",
			TS:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			Probe: *probeID, Type: typ, From: from, To: to, Note: note,
		})
	}
	if identity.Changed(prev, id) {
		writeEvent("asn_changed", prev.ASN, id.ASN, "uplink moved to a different autonomous system")
	}
	if !fresh && id.ASN == "unknown" {
		writeEvent("identity_unknown", "", "", "both geolocation providers unreachable; run continues without an ASN")
	}

	stamp := func(m *schema.Measurement, t targets.Target, attempt int) {
		m.RunID, m.TS = slot, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		m.Probe, m.Net = *probeID, *netType
		m.ASN, m.Country, m.Region = id.ASN, id.Country, id.Region
		m.Profile, m.List, m.Category = *profile, t.List, t.Category
		m.Target, m.Attempt = t.Host, attempt
	}

	var (
		mu         sync.Mutex
		byVerdict  = map[string]int{}
		rows, ok   int
		ctrlOK     int
		ctrlIntlOK int
		failed     []targets.Target
	)
	record := func(m *schema.Measurement, t targets.Target) {
		mu.Lock()
		rows++
		byVerdict[m.Verdict]++
		if m.Verdict == classify.VOK {
			ok++
			if t.IsControl() {
				ctrlOK++
				if t.IsIntlControl() {
					ctrlIntlOK++
				}
			}
		} else if m.Attempt == 1 {
			failed = append(failed, t)
		}
		mu.Unlock()
		if err := mw.Write(m); err != nil {
			log.Printf("write row: %v", err)
		}
	}

	opts := tests.DefaultOptions()
	opts.Family = *family
	measure := func(list []targets.Target, attempt int) {
		sem := make(chan struct{}, *conc)
		var wg sync.WaitGroup
		for _, t := range list {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(t targets.Target) {
				defer wg.Done()
				defer func() { <-sem }()
				// Jitter keeps the workers from marching in lockstep, which
				// would otherwise put a visible periodic pattern on the wire.
				time.Sleep(time.Duration(rand.Intn(250)) * time.Millisecond) //nolint:gosec
				m := tests.Reach(ctx, t.URL, opts)
				stamp(m, t, attempt)
				record(m, t)
			}(t)
		}
		wg.Wait()
	}

	ctrlTotal, ctrlIntlTotal := set.Controls()
	log.Printf("run %s: %d targets (%d controls, %d of them international), probe %s on %s",
		slot, len(set.Targets), ctrlTotal, ctrlIntlTotal, *probeID, id)
	measure(set.Targets, 1)

	// Second pass: a single confirmation retry, only for what failed, and only
	// when most of the run succeeded. A failure that reproduces immediately is
	// much stronger evidence than a single observation.
	retried := 0
	if n := len(set.Targets); n > 0 && float64(len(failed))/float64(n) <= retryCeiling && ctx.Err() == nil {
		time.Sleep(3 * time.Second)
		retried = len(failed)
		measure(failed, 2)
	}

	finished := time.Now().UTC()
	// Judged on the international controls: those must answer any probe on the
	// planet, so failing them means this probe had no usable network and the
	// run says nothing about filtering.
	healthy := ctrlIntlTotal == 0 || float64(ctrlIntlOK) >= 0.5*float64(ctrlIntlTotal)
	run := schema.Run{
		Schema: schema.Version, Kind: "run", RunID: slot,
		Probe: *probeID, Net: *netType,
		ASN: id.ASN, Country: id.Country, Region: id.Region,
		Agent: schema.AgentVersion, Profile: *profile,
		StartedAt:  started.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt: finished.Format("2006-01-02T15:04:05.000Z"),
		DurationS:  finished.Sub(started).Seconds(),
		Targets:    len(set.Targets), Rows: rows, OK: ok, Failed: rows - ok,
		ByVerdict:     byVerdict,
		ControlsTotal: ctrlTotal, ControlsOK: ctrlOK,
		ControlsIntlTotal: ctrlIntlTotal, ControlsIntlOK: ctrlIntlOK,
		Healthy:      healthy,
		ListManifest: set.Manifest,
	}
	if err := rw.Write(run); err != nil {
		log.Printf("write run: %v", err)
	}

	if n, _ := jsonl.Seal(*dataDir); n > 0 {
		log.Printf("sealed %d finished day file(s)", n)
	}
	if n, _ := jsonl.Prune(*dataDir, *keepDays); n > 0 {
		log.Printf("pruned %d file(s) older than %d days", n, *keepDays)
	}

	log.Printf("run %s done in %.0fs: %d rows, %d ok, %d failed, %d retried, controls %d/%d (intl %d/%d), healthy=%v",
		slot, run.DurationS, rows, ok, run.Failed, retried, ctrlOK, ctrlTotal, ctrlIntlOK, ctrlIntlTotal, healthy)
	if !healthy {
		// Exit non-zero so systemd marks the unit failed and the outage is
		// visible in the journal as well as in the data.
		os.Exit(3)
	}
}

type sniArgs struct {
	probeID, netType, family string
	dataDir, ip, port        string
	runID                    string
	repeats                  int
	started                  time.Time
	asn, country, region     string
}

// runSNI drives the same-address-different-name experiment and writes its rows
// into the ordinary measurement stream, tagged list "sni".
//
// The run id is the wall-clock minute rather than a six-hour slot: this is a
// manually driven experiment, and two probes running it are expected to be
// started together by hand. Pass the same -run-id on both to force a join.
func runSNI(ctx context.Context, a sniArgs) {
	mw, err := jsonl.New(a.dataDir, "measurements")
	if err != nil {
		log.Fatalf("open measurements: %v", err)
	}
	defer mw.Close()
	rw, err := jsonl.New(a.dataDir, "runs")
	if err != nil {
		log.Fatalf("open runs: %v", err)
	}
	defer rw.Close()

	// Truncated to fifteen minutes so two probes started by hand within the
	// same quarter hour share an id and their rows join. -run-id overrides it
	// when an exact pairing is wanted.
	runID := a.runID
	if runID == "" {
		runID = a.started.Truncate(15*time.Minute).Format("2006-01-02T15:04Z") + "/sni"
	}
	opts := tests.DefaultOptions()
	opts.Family = a.family

	log.Printf("run %s: sni experiment against %s:%s, %d names x %d rounds, probe %s",
		runID, a.ip, a.port, len(tests.DefaultSNITrials), a.repeats, a.probeID)

	rows := tests.SNIExperiment(ctx, a.ip, a.port, a.repeats, tests.DefaultSNITrials, opts)

	byVerdict := map[string]int{}
	ok := 0
	for _, m := range rows {
		m.RunID = runID
		m.TS = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		m.Probe, m.Net = a.probeID, a.netType
		m.ASN, m.Country, m.Region = a.asn, a.country, a.region
		m.Profile = "sni"
		byVerdict[m.Verdict]++
		if m.Verdict == classify.VOK {
			ok++
		}
		if err := mw.Write(m); err != nil {
			log.Printf("write row: %v", err)
		}
	}

	finished := time.Now().UTC()
	_ = rw.Write(schema.Run{
		Schema: schema.Version, Kind: "run", RunID: runID,
		Probe: a.probeID, Net: a.netType,
		ASN: a.asn, Country: a.country, Region: a.region,
		Agent: schema.AgentVersion, Profile: "sni",
		StartedAt:  a.started.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt: finished.Format("2006-01-02T15:04:05.000Z"),
		DurationS:  finished.Sub(a.started).Seconds(),
		Targets:    len(tests.DefaultSNITrials), Rows: len(rows), OK: ok, Failed: len(rows) - ok,
		ByVerdict: byVerdict,
		// No connectivity controls in this experiment: it targets a single
		// address we own, so a control set measuring other hosts would say
		// nothing about whether this particular tuple was reachable.
		Healthy: true,
	})

	// A compact summary on stdout, because this experiment is read by a human
	// the moment it finishes, not only from the archive.
	log.Printf("run %s done in %.0fs", runID, finished.Sub(a.started).Seconds())
	agg := map[string]map[string]int{}
	for _, m := range rows {
		if agg[m.Target] == nil {
			agg[m.Target] = map[string]int{}
		}
		agg[m.Target][m.Verdict]++
	}
	names := make([]string, 0, len(agg))
	for n := range agg {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var parts []string
		for v, c := range agg[n] {
			parts = append(parts, fmt.Sprintf("%s=%d", v, c))
		}
		sort.Strings(parts)
		fmt.Printf("%-26s %s\n", n, strings.Join(parts, " "))
	}
}

func runDry(ctx context.Context, set *targets.Set, n int, family string) {
	opts := tests.DefaultOptions()
	opts.Family = family
	if n > len(set.Targets) {
		n = len(set.Targets)
	}
	for _, t := range set.Targets[:n] {
		m := tests.Reach(ctx, t.URL, opts)
		fmt.Printf("%-40s %-18s stage=%-8s http=%-3d rc=%-2d %-2s %-15s read=%-7d t=%.2fs %s\n",
			trim(t.URL, 40), m.Verdict, m.Stage, m.HTTP, m.CurlRC, m.IPFamily, m.RemoteIP, m.BytesRead, m.TTotal, trim(m.Title, 36))
	}
}

// loadOwn reads our own endpoints. They live outside the pinned lists because
// they change when we deploy a responder, not when the public list is updated.
func loadOwn(path string) ([]targets.Target, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []targets.Target
	for i, row := range rows {
		if i == 0 || len(row) == 0 || strings.HasPrefix(row[0], "#") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(row[0]))
		if err != nil || u.Host == "" {
			continue
		}
		t := targets.Target{URL: row[0], Host: u.Hostname(), List: "own", Category: "OWN"}
		if len(row) > 1 {
			t.Category = strings.TrimSpace(row[1])
		}
		out = append(out, t)
	}
	return out, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
