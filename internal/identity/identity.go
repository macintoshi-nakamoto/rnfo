// Package identity establishes what network the probe is currently on.
//
// Ethics constraint, see docs/ETHICS.md: the published dataset stores ASN and
// region, never an address. The probe's own address is kept in a local cache
// file so that a change can be detected, and that file is never shipped. What
// reaches the dataset is the ASN, the country, the region and an event saying
// the network changed.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Identity is what we know about the probe's current uplink.
type Identity struct {
	ASN     string `json:"asn"`     // "AS203273 NetCrafters OU"
	Country string `json:"country"` // "RU"
	Region  string `json:"region"`  // "Moscow"
	City    string `json:"city"`

	// IP is cached locally to detect a change on a dynamic line. It is
	// deliberately not part of any record written to the dataset.
	IP        string `json:"ip"`
	CheckedAt string `json:"checked_at"`
	Source    string `json:"source"`
}

// TTL is how long a cached identity is trusted before we look again. A
// residential line can renumber at any time, so this is short enough to catch
// it within one full-profile slot.
const TTL = 3 * time.Hour

type source struct {
	name string
	url  string
	// parse maps the provider's JSON onto our fields.
	parse func(map[string]any) Identity
}

// Two independent providers, tried in order. Neither is load-bearing: if both
// fail the probe keeps measuring with the cached identity and records an event,
// because losing the ASN lookup must never cost us a slot of data.
var sources = []source{
	{
		name: "ipinfo.io",
		url:  "https://ipinfo.io/json",
		parse: func(m map[string]any) Identity {
			return Identity{
				ASN:     str(m["org"]),
				Country: str(m["country"]),
				Region:  str(m["region"]),
				City:    str(m["city"]),
				IP:      str(m["ip"]),
			}
		},
	},
	{
		name: "ifconfig.co",
		url:  "https://ifconfig.co/json",
		parse: func(m map[string]any) Identity {
			asn := strings.TrimSpace(str(m["asn"]) + " " + str(m["asn_org"]))
			return Identity{
				ASN:     asn,
				Country: str(m["country_iso"]),
				Region:  str(m["region_name"]),
				City:    str(m["city"]),
				IP:      str(m["ip"]),
			}
		},
	},
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// Resolve returns the current identity, the previous cached one, and whether
// the lookup succeeded this time. The caller compares the two to decide
// whether to write an asn_changed event.
func Resolve(ctx context.Context, stateDir string) (cur, prev Identity, fresh bool) {
	path := filepath.Join(stateDir, "identity.json")
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &prev)
	}
	if prev.CheckedAt != "" {
		if t, err := time.Parse(time.RFC3339, prev.CheckedAt); err == nil && time.Since(t) < TTL {
			return prev, prev, false
		}
	}

	client := &http.Client{Timeout: 12 * time.Second}
	for _, s := range sources {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "rnfo-probe (network reachability measurement)")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var m map[string]any
		err = json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if err != nil {
			continue
		}
		id := s.parse(m)
		if id.ASN == "" && id.IP == "" {
			continue
		}
		id.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		id.Source = s.name
		save(path, id)
		return id, prev, true
	}
	// Both providers unreachable. That is itself informative from inside a
	// filtered network, so the caller records it, but the run continues on the
	// last known identity.
	if prev.ASN != "" {
		return prev, prev, false
	}
	return Identity{ASN: "unknown", Country: "??", Region: ""}, prev, false
}

func save(path string, id Identity) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// Changed reports whether the uplink moved to a different network. An address
// change on the same ASN is normal on a residential line and is not an event;
// a different ASN means the probe is measuring a different operator, which
// invalidates comparisons across the boundary.
func Changed(prev, cur Identity) bool {
	return prev.ASN != "" && cur.ASN != "" && prev.ASN != cur.ASN
}

// String renders the identity for logs.
func (i Identity) String() string {
	return fmt.Sprintf("%s / %s / %s", i.ASN, i.Country, i.Region)
}
