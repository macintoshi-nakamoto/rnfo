// Package targets loads and orders the target sets a run measures.
package targets

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/url"
	"sort"
	"strings"
)

// Target is one URL to measure, with the provenance needed to interpret it.
type Target struct {
	URL      string
	Host     string
	List     string // citizenlab-global | citizenlab-ru | controls | own
	Category string // Citizen Lab category code
	Scope    string // controls only: "ru" or "intl"
}

// IsControl reports whether this target is part of the connectivity control
// set. Controls are not subjects of the study; they are how we tell "the
// network filtered this" apart from "the probe had no network".
func (t Target) IsControl() bool { return t.List == "controls" }

// Profiles decide which lists a run covers. The full profile is the study;
// the controls profile runs far more often and exists to give downtime and
// local outages a fine time resolution.
var Profiles = map[string][]string{
	"full":     {"controls", "own", "citizenlab-ru", "citizenlab-global"},
	"controls": {"controls", "own"},
}

// Set is the loaded target set for one run, plus the checksums of the files
// it came from.
type Set struct {
	Targets  []Target
	Manifest map[string]string // list name -> sha256 of the source file
}

// Load reads the requested lists out of fsys. own holds our own endpoints,
// supplied by the caller because they change independently of the pinned
// public lists.
func Load(fsys fs.FS, profile string, own []Target) (*Set, error) {
	names, ok := Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	s := &Set{Manifest: map[string]string{}}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "own" {
			for _, t := range own {
				t.List = "own"
				if add(s, seen, t) {
					continue
				}
			}
			continue
		}
		f, err := fsys.Open(name + ".csv")
		if err != nil {
			return nil, fmt.Errorf("open list %s: %w", name, err)
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		s.Manifest[name] = hex.EncodeToString(sum[:])
		ts, err := parseCSV(string(raw), name)
		if err != nil {
			return nil, fmt.Errorf("parse list %s: %w", name, err)
		}
		for _, t := range ts {
			add(s, seen, t)
		}
	}
	return s, nil
}

func add(s *Set, seen map[string]bool, t Target) bool {
	if t.URL == "" || seen[t.URL] {
		return false
	}
	seen[t.URL] = true
	s.Targets = append(s.Targets, t)
	return true
}

// parseCSV reads the Citizen Lab test list format:
// url,category_code,category_description,date_added,source,notes
func parseCSV(body, list string) ([]Target, error) {
	r := csv.NewReader(strings.NewReader(body))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []Target
	for i, row := range rows {
		if i == 0 || len(row) == 0 {
			continue // header
		}
		raw := strings.TrimSpace(row[0])
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue // a malformed entry upstream is skipped, not fatal
		}
		t := Target{URL: raw, Host: u.Hostname(), List: list}
		if len(row) > 1 {
			t.Category = strings.TrimSpace(row[1])
		}
		if list == "controls" && len(row) > 5 {
			t.Scope = strings.TrimSpace(row[5])
		}
		out = append(out, t)
	}
	return out, nil
}

// Order sorts the set deterministically and then shuffles it with a seed
// derived from the run id.
//
// Two properties matter here. The order is identical on every probe in the
// same slot, so a Russian measurement and its foreign control hit a given
// target at roughly the same moment. And the order differs between slots, so
// no site is permanently measured first, which would otherwise correlate
// every result for that site with whatever happens at the start of a run.
func (s *Set) Order(runID string) {
	sort.Slice(s.Targets, func(i, j int) bool {
		if s.Targets[i].List != s.Targets[j].List {
			return s.Targets[i].List < s.Targets[j].List
		}
		return s.Targets[i].URL < s.Targets[j].URL
	})
	// Controls run first and are excluded from the shuffle: if the probe has
	// no network, we want to know within the first few seconds of a run.
	var ctrl, rest []Target
	for _, t := range s.Targets {
		if t.IsControl() {
			ctrl = append(ctrl, t)
		} else {
			rest = append(rest, t)
		}
	}
	sum := sha256.Sum256([]byte(runID))
	rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(sum[:8])))) //nolint:gosec // reproducibility, not secrecy
	rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	s.Targets = append(ctrl, rest...)
}

// IsIntlControl reports whether this is a control that must be reachable from
// anywhere in the world. Domestic controls are not: a national portal may
// legitimately refuse a foreign address.
func (t Target) IsIntlControl() bool { return t.IsControl() && t.Scope == "intl" }

// Controls returns how many targets in the set are controls, and how many of
// those are international.
func (s *Set) Controls() (total, intl int) {
	for _, t := range s.Targets {
		if t.IsControl() {
			total++
			if t.IsIntlControl() {
				intl++
			}
		}
	}
	return total, intl
}
