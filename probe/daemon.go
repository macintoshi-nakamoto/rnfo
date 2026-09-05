package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/jsonl"
	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
)

// The daemon is systemd's job done by the binary itself, for hosts that have
// no systemd: an Android handset under Termux, or anything else that can only
// keep one long-lived process alive.
//
// It reproduces the three properties the timers provide, because the data
// must not be able to tell which scheduler produced it:
//
//   - fixed UTC slot boundaries (00/06/12/18 for full, every quarter hour for
//     controls), computed from the wall clock, so run ids line up with every
//     other probe;
//   - Persistent=true semantics: if the process starts and the current slot
//     has not run yet, it runs immediately under that slot's id, so downtime
//     shows up as a late run rather than as silence;
//   - no self-overlap: a slot whose previous run is still going is skipped and
//     recorded as an event, as systemd would refuse to start a second instance.
//
// Both profiles run concurrently, as they do under systemd, sharing the
// writers so that appends to one day file come from one process.

// daemonState survives restarts; it is the equivalent of systemd's persistent
// timer stamps. It lives in the state directory, which is never shipped.
type daemonState struct {
	LastFull     string `json:"last_full"`
	LastControls string `json:"last_controls"`
}

func runDaemon(ctx context.Context, cfg config, profiles []string) {
	mw, rw := openWriters(cfg)
	defer mw.Close()
	defer rw.Close()

	statePath := filepath.Join(cfg.stateDir, "daemon.json")
	st := loadDaemonState(statePath)
	var stMu sync.Mutex
	save := func() {
		stMu.Lock()
		defer stMu.Unlock()
		if err := os.MkdirAll(cfg.stateDir, 0o750); err != nil {
			return
		}
		b, _ := json.MarshalIndent(st, "", "  ")
		tmp := statePath + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, statePath)
		}
	}

	_ = rw.Write(schema.Event{
		Schema: schema.Version, Kind: "event",
		TS:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Probe: cfg.probeID, Type: "agent_started",
		Note: "daemon mode, " + schema.AgentVersion,
	})

	log.Printf("daemon: probe %s, slots full=%s controls=%s, last full=%q controls=%q",
		cfg.probeID, slotPeriod["full"], slotPeriod["controls"], st.LastFull, st.LastControls)

	var wg sync.WaitGroup
	for _, profile := range profiles {
		wg.Add(1)
		go func(profile string) {
			defer wg.Done()
			last := func() string {
				stMu.Lock()
				defer stMu.Unlock()
				if profile == "full" {
					return st.LastFull
				}
				return st.LastControls
			}
			setLast := func(id string) {
				stMu.Lock()
				if profile == "full" {
					st.LastFull = id
				} else {
					st.LastControls = id
				}
				stMu.Unlock()
				save()
			}
			scheduleProfile(ctx, cfg, profile, mw, rw, last, setLast)
		}(profile)
	}
	wg.Wait()
	log.Printf("daemon: stopped")
}

// scheduleProfile runs one profile on its slot boundaries until ctx ends.
func scheduleProfile(ctx context.Context, cfg config, profile string, mw, rw *jsonl.Writer,
	last func() string, setLast func(string)) {
	period := slotPeriod[profile]
	var running sync.Mutex // held while a run of this profile is in progress

	fire := func(slot string) {
		if !running.TryLock() {
			// The previous run of this profile has not finished. systemd would
			// not start a second instance either; record it and move on.
			_ = rw.Write(schema.Event{
				Schema: schema.Version, Kind: "event",
				TS:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				Probe: cfg.probeID, Type: "run_skipped_overlap", To: slot,
				Note: "previous " + profile + " run still in progress",
			})
			log.Printf("daemon: %s slot %s skipped, previous run still going", profile, slot)
			return
		}
		setLast(slot)
		go func() {
			defer running.Unlock()
			runOnce(ctx, cfg, profile, slot, mw, rw)
		}()
	}

	// Catch-up on start: the current slot, if it has not been run by this
	// probe yet. This is what Persistent=true does for a timer.
	now := time.Now().UTC()
	current := slotID(now, profile)
	if last() != current {
		log.Printf("daemon: %s slot %s not yet run, firing now (catch-up)", profile, current)
		fire(current)
	}

	for {
		next := now.Truncate(period).Add(period)
		wait := time.Until(next)
		select {
		case <-ctx.Done():
			// Let a run in flight finish writing; runOnce honours ctx too, so
			// this is bounded by one measurement timeout.
			running.Lock()
			running.Unlock()
			return
		case <-time.After(wait):
		}
		now = time.Now().UTC()
		slot := slotID(next, profile)
		fire(slot)
	}
}

func loadDaemonState(path string) daemonState {
	var st daemonState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}
