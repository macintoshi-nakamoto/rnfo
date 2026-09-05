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
//
// Two ways to run it. On a host with systemd, timers start one process per
// run (-profile full|controls) and it exits. On a host without systemd - an
// Android handset under Termux - the same binary runs as -daemon and keeps its
// own slot-aligned schedule, so the rows it writes are indistinguishable from
// the timer-driven ones.
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net"
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

// config is everything a run needs that does not change between runs.
type config struct {
	probeID  string
	netType  string
	dataDir  string
	stateDir string
	ownFile  string
	family   string
	dns      string // "" = system resolver; otherwise host:port of a resolver to use
	conc     int
	keepDays int
	maxBody  int64
}

func main() {
	var (
		cfg      config
		profile  = flag.String("profile", "full", "target profile: full or controls")
		exper    = flag.String("experiment", "", "run a one-off experiment instead of a scheduled measurement: sni")
		respIP   = flag.String("responder", env("RNFO_RESPONDER_IP", ""), "responder address for -experiment")
		respPort = flag.String("responder-port", env("RNFO_RESPONDER_PORT", "443"), "responder port for -experiment")
		repeats  = flag.Int("repeats", 5, "rounds per name in -experiment sni")
		runID    = flag.String("run-id", "", "override the slot-derived run id")
		daemon   = flag.Bool("daemon", false, "keep running and fire profiles on their slot boundaries (for hosts without systemd)")
		daemonP  = flag.String("daemon-profiles", env("RNFO_DAEMON_PROFILES", "controls,full"), "profiles the daemon schedules")
		whoami   = flag.Bool("whoami", false, "print the network this probe is on and exit; used to commission a probe")
		dryRun   = flag.Bool("dry-run", false, "measure a handful of targets and print rows to stdout")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.StringVar(&cfg.probeID, "probe", env("RNFO_PROBE_ID", ""), "probe id, must exist in probes.yaml")
	flag.StringVar(&cfg.netType, "net", env("RNFO_NET", ""), "network type: hosting, eyeball or mobile")
	flag.StringVar(&cfg.dataDir, "data", env("RNFO_DATA_DIR", "/var/lib/rnfo/data"), "data directory")
	flag.StringVar(&cfg.stateDir, "state", env("RNFO_STATE_DIR", "/var/lib/rnfo/state"), "state directory, never shipped")
	flag.StringVar(&cfg.ownFile, "own", env("RNFO_OWN_TARGETS", ""), "optional CSV of our own endpoints")
	flag.StringVar(&cfg.family, "family", env("RNFO_IP_FAMILY", "v4"), "address family to measure over: v4, v6 or auto")
	flag.StringVar(&cfg.dns, "dns", env("RNFO_DNS", ""), "resolver host:port to use instead of the system resolver (Termux has no /etc/resolv.conf)")
	flag.IntVar(&cfg.conc, "concurrency", envInt("RNFO_CONCURRENCY", 12), "parallel measurements")
	flag.IntVar(&cfg.keepDays, "keep-days", envInt("RNFO_KEEP_DAYS", 90), "days of local data to retain")
	flag.Int64Var(&cfg.maxBody, "max-body", envInt64("RNFO_MAX_BODY", 2<<20), "bytes of response body to read per target (lower on metered links)")
	flag.Parse()

	if *showVer {
		fmt.Println(schema.AgentVersion, "schema", schema.Version)
		return
	}
	if cfg.probeID == "" || cfg.netType == "" {
		log.Fatal("probe id and net type are required (-probe, -net or RNFO_PROBE_ID, RNFO_NET)")
	}
	switch cfg.family {
	case "v4", "v6", "auto":
	default:
		log.Fatal("family must be v4, v6 or auto")
	}
	switch cfg.netType {
	case schema.NetHosting, schema.NetEyeball, schema.NetMobile:
	default:
		log.Fatalf("net must be one of %s, %s, %s", schema.NetHosting, schema.NetEyeball, schema.NetMobile)
	}
	if _, ok := targets.Profiles[*profile]; !ok {
		log.Fatalf("unknown profile %q", *profile)
	}
	if cfg.maxBody < 64<<10 {
		// Below the hash window the body hash would no longer mean the same
		// thing across probes. Refuse rather than silently produce it.
		log.Fatal("max-body must be at least 65536 bytes (the hash window)")
	}
	installResolver(cfg.dns)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch {
	case *whoami:
		// Commissioning check. A probe whose traffic leaves through the
		// operator's own tunnel measures the tunnel, not the network it is
		// supposed to represent (the vantage point rule (docs/METHODOLOGY.md §5)), and the only way to
		// know is to ask the outside what address is talking to it.
		id, _, _ := identity.Resolve(ctx, cfg.stateDir)
		fmt.Printf("asn=%s\ncountry=%s\nregion=%s\ncity=%s\n", id.ASN, id.Country, id.Region, id.City)
		return

	case *exper != "":
		if *exper != "sni" {
			log.Fatalf("unknown experiment %q", *exper)
		}
		if *respIP == "" {
			log.Fatal("-responder <ip> is required for -experiment sni")
		}
		runSNI(ctx, cfg, *respIP, *respPort, *repeats, *runID)
		return

	case *dryRun:
		set := loadSet(cfg, *profile)
		set.Order("dry-run")
		runDry(ctx, set, 8, cfg)
		return

	case *daemon:
		var profiles []string
		for _, p := range strings.Split(*daemonP, ",") {
			if p = strings.TrimSpace(p); p != "" {
				if _, ok := targets.Profiles[p]; !ok {
					log.Fatalf("unknown daemon profile %q", p)
				}
				profiles = append(profiles, p)
			}
		}
		runDaemon(ctx, cfg, profiles)
		return
	}

	mw, rw := openWriters(cfg)
	defer mw.Close()
	defer rw.Close()
	if !runOnce(ctx, cfg, *profile, *runID, mw, rw) {
		// Exit non-zero so systemd marks the unit failed and the outage is
		// visible in the journal as well as in the data.
		os.Exit(3)
	}
}

// installResolver replaces the process resolver when -dns is given.
//
// Why this exists: a static Go binary resolves names by reading
// /etc/resolv.conf. Termux on Android has no such file and no way to create
// one without root, so the default resolver silently points at localhost and
// every lookup fails. Pointing at the home router keeps the ISP's resolver in
// the path, which is what an eyeball measurement should see; pointing at a
// public resolver would change what is being measured, and the run record
// says which was used.
func installResolver(addr string) {
	if addr == "" {
		return
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "53")
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
}

func openWriters(cfg config) (*jsonl.Writer, *jsonl.Writer) {
	mw, err := jsonl.New(cfg.dataDir, "measurements")
	if err != nil {
		log.Fatalf("open measurements: %v", err)
	}
	rw, err := jsonl.New(cfg.dataDir, "runs")
	if err != nil {
		log.Fatalf("open runs: %v", err)
	}
	return mw, rw
}

func loadSet(cfg config, profile string) *targets.Set {
	own, err := loadOwn(cfg.ownFile)
	if err != nil {
		log.Fatalf("own targets: %v", err)
	}
	sub, err := fs.Sub(lists.FS, ".")
	if err != nil {
		log.Fatalf("lists: %v", err)
	}
	set, err := targets.Load(sub, profile, own)
	if err != nil {
		log.Fatalf("targets: %v", err)
	}
	return set
}

func slotID(t time.Time, profile string) string {
	return t.UTC().Truncate(slotPeriod[profile]).Format("2006-01-02T15:04Z") + "/" + profile
}

// runOnce performs one scheduled measurement of one profile and returns
// whether the probe was healthy for it. It is the unit of work for both the
// systemd path and the daemon path, so the two produce identical records.
func runOnce(ctx context.Context, cfg config, profile, runID string, mw, rw *jsonl.Writer) bool {
	started := time.Now().UTC()
	slot := runID
	if slot == "" {
		slot = slotID(started, profile)
	}

	// Identity first: a run whose network we cannot name is still worth
	// having, but the ambiguity must be recorded rather than guessed at.
	id, prev, fresh := identity.Resolve(ctx, cfg.stateDir)

	set := loadSet(cfg, profile)
	set.Order(slot)

	writeEvent := func(typ, from, to, note string) {
		_ = rw.Write(schema.Event{
			Schema: schema.Version, Kind: "event",
			TS:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			Probe: cfg.probeID, Type: typ, From: from, To: to, Note: note,
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
		m.Probe, m.Net = cfg.probeID, cfg.netType
		m.ASN, m.Country, m.Region = id.ASN, id.Country, id.Region
		m.Profile, m.List, m.Category = profile, t.List, t.Category
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
	opts.Family = cfg.family
	opts.MaxBody = cfg.maxBody
	measure := func(list []targets.Target, attempt int) {
		sem := make(chan struct{}, cfg.conc)
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
		slot, len(set.Targets), ctrlTotal, ctrlIntlTotal, cfg.probeID, id)
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
		Probe: cfg.probeID, Net: cfg.netType,
		ASN: id.ASN, Country: id.Country, Region: id.Region,
		Agent: schema.AgentVersion, Profile: profile,
		StartedAt:  started.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt: finished.Format("2006-01-02T15:04:05.000Z"),
		DurationS:  finished.Sub(started).Seconds(),
		Targets:    len(set.Targets), Rows: rows, OK: ok, Failed: rows - ok,
		ByVerdict:     byVerdict,
		ControlsTotal: ctrlTotal, ControlsOK: ctrlOK,
		ControlsIntlTotal: ctrlIntlTotal, ControlsIntlOK: ctrlIntlOK,
		Healthy:      healthy,
		ListManifest: set.Manifest,
		Resolver:     cfg.dns,
		MaxBody:      cfg.maxBody,
	}
	if err := rw.Write(run); err != nil {
		log.Printf("write run: %v", err)
	}

	if n, _ := jsonl.Seal(cfg.dataDir); n > 0 {
		log.Printf("sealed %d finished day file(s)", n)
	}
	if n, _ := jsonl.Prune(cfg.dataDir, cfg.keepDays); n > 0 {
		log.Printf("pruned %d file(s) older than %d days", n, cfg.keepDays)
	}

	log.Printf("run %s done in %.0fs: %d rows, %d ok, %d failed, %d retried, controls %d/%d (intl %d/%d), healthy=%v",
		slot, run.DurationS, rows, ok, run.Failed, retried, ctrlOK, ctrlTotal, ctrlIntlOK, ctrlIntlTotal, healthy)
	return healthy
}

// runSNI drives the same-address-different-name experiment and writes its rows
// into the ordinary measurement stream, tagged list "sni", as each completes.
func runSNI(ctx context.Context, cfg config, ip, port string, repeats int, runID string) {
	mw, rw := openWriters(cfg)
	defer mw.Close()
	defer rw.Close()

	started := time.Now().UTC()
	id, _, _ := identity.Resolve(ctx, cfg.stateDir)

	// Truncated to fifteen minutes so two probes started by hand within the
	// same quarter hour share an id and their rows join. -run-id overrides it
	// when an exact pairing is wanted.
	if runID == "" {
		runID = started.Truncate(15*time.Minute).Format("2006-01-02T15:04Z") + "/sni"
	}
	opts := tests.DefaultOptions()
	opts.Family = cfg.family
	opts.MaxBody = cfg.maxBody

	log.Printf("run %s: sni experiment against %s:%s, %d names x %d rounds, probe %s",
		runID, ip, port, len(tests.DefaultSNITrials), repeats, cfg.probeID)

	byVerdict := map[string]int{}
	ok := 0
	emit := func(m *schema.Measurement) {
		m.RunID = runID
		m.TS = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		m.Probe, m.Net = cfg.probeID, cfg.netType
		m.ASN, m.Country, m.Region = id.ASN, id.Country, id.Region
		m.Profile = "sni"
		byVerdict[m.Verdict]++
		if m.Verdict == classify.VOK {
			ok++
		}
		if err := mw.Write(m); err != nil {
			log.Printf("write row: %v", err)
		}
		log.Printf("  %-26s round %d  %s", m.Target, m.Attempt, m.Verdict)
	}
	rows := tests.SNIExperiment(ctx, ip, port, repeats, tests.DefaultSNITrials, opts, emit)

	finished := time.Now().UTC()
	_ = rw.Write(schema.Run{
		Schema: schema.Version, Kind: "run", RunID: runID,
		Probe: cfg.probeID, Net: cfg.netType,
		ASN: id.ASN, Country: id.Country, Region: id.Region,
		Agent: schema.AgentVersion, Profile: "sni",
		StartedAt:  started.Format("2006-01-02T15:04:05.000Z"),
		FinishedAt: finished.Format("2006-01-02T15:04:05.000Z"),
		DurationS:  finished.Sub(started).Seconds(),
		Targets:    len(tests.DefaultSNITrials), Rows: len(rows), OK: ok, Failed: len(rows) - ok,
		ByVerdict: byVerdict,
		// No connectivity controls in this experiment: it targets a single
		// address we own, so a control set measuring other hosts would say
		// nothing about whether this particular tuple was reachable.
		Healthy:  true,
		Resolver: cfg.dns,
		MaxBody:  cfg.maxBody,
	})

	log.Printf("run %s done in %.0fs", runID, finished.Sub(started).Seconds())
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

func runDry(ctx context.Context, set *targets.Set, n int, cfg config) {
	opts := tests.DefaultOptions()
	opts.Family = cfg.family
	opts.MaxBody = cfg.maxBody
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

func envInt64(k string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(k), 10, 64); err == nil && v > 0 {
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
