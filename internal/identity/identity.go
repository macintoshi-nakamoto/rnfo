// Package identity establishes what network the probe is currently on.
//
// Ethics constraint, see docs/ETHICS.md: the published dataset stores ASN and
// region, never an address. The probe's own address is kept in a local cache
// file so that a change can be detected, and that file is never shipped. What
// reaches the dataset is the ASN, the country, the region and an event saying
// the network changed.
//
// Two ways of finding out, tried in order. First plain DNS: a resolver that
// answers "myip" with the address it sees, then Team Cymru's TXT service to map
// that address to an ASN. This needs no TLS, no CA store and no JSON, so it
// works from a Termux handset and from a network that dislikes API hosts. Then
// the HTTPS providers, for the region and city they add. Neither is
// load-bearing: if everything fails the probe keeps measuring with the cached
// identity and records an event, because losing the ASN lookup must never cost
// a slot of data.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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

// myIPSources answer a DNS query with the address the query came from. Two
// independent operators, so one being unreachable is not fatal.
var myIPSources = []struct {
	name, server, question string
	txt                    bool
}{
	{"opendns", "208.67.222.222:53", "myip.opendns.com", false},
	{"google", "216.239.32.10:53", "o-o.myaddr.l.google.com", true},
}

// httpSources add region and city, which DNS cannot.
var httpSources = []struct {
	name  string
	url   string
	parse func(map[string]any) Identity
}{
	{
		name: "ipinfo.io",
		url:  "https://ipinfo.io/json",
		parse: func(m map[string]any) Identity {
			return Identity{ASN: str(m["org"]), Country: str(m["country"]), Region: str(m["region"]), City: str(m["city"]), IP: str(m["ip"])}
		},
	},
	{
		name: "ifconfig.co",
		url:  "https://ifconfig.co/json",
		parse: func(m map[string]any) Identity {
			return Identity{ASN: strings.TrimSpace(str(m["asn"]) + " " + str(m["asn_org"])), Country: str(m["country_iso"]), Region: str(m["region_name"]), City: str(m["city"]), IP: str(m["ip"])}
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

	id, ok := viaDNS(ctx)
	if ok {
		// Enrich with region and city if any HTTPS provider answers quickly.
		if h, hok := viaHTTP(ctx, 8*time.Second); hok && h.IP == id.IP {
			id.Region, id.City = h.Region, h.City
			if id.ASN == "" {
				id.ASN = h.ASN
			}
			id.Source += "+" + h.Source
		}
	} else {
		id, ok = viaHTTP(ctx, 12*time.Second)
	}
	if ok {
		id.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		save(path, id)
		return id, prev, true
	}
	// Everything unreachable. That is itself informative from inside a
	// filtered network, so the caller records it, but the run continues on the
	// last known identity.
	if prev.ASN != "" {
		return prev, prev, false
	}
	return Identity{ASN: "unknown", Country: "??", Region: ""}, prev, false
}

// viaDNS finds the public address and its ASN with plain DNS only.
func viaDNS(ctx context.Context) (Identity, bool) {
	var ip, src string
	for _, s := range myIPSources {
		c, cancel := context.WithTimeout(ctx, 4*time.Second)
		r := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "udp", s.server)
			},
		}
		if s.txt {
			if txt, err := r.LookupTXT(c, s.question); err == nil && len(txt) > 0 && net.ParseIP(strings.Trim(txt[0], `"`)) != nil {
				ip, src = strings.Trim(txt[0], `"`), s.name
			}
		} else if ips, err := r.LookupIPAddr(c, s.question); err == nil && len(ips) > 0 {
			ip, src = ips[0].IP.String(), s.name
		}
		cancel()
		if ip != "" {
			break
		}
	}
	if ip == "" {
		return Identity{}, false
	}
	id := Identity{IP: ip, Source: "dns:" + src}

	// Team Cymru: reversed address under origin.asn.cymru.com gives
	// "ASN | prefix | CC | registry | date"; AS<n>.asn.cymru.com gives the name.
	p := net.ParseIP(ip).To4()
	if p == nil {
		return id, true // v6 could be handled too; the probes measure v4
	}
	rev := fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", p[3], p[2], p[1], p[0])
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	txt, err := net.DefaultResolver.LookupTXT(c, rev)
	if err != nil || len(txt) == 0 {
		return id, true
	}
	f := strings.Split(txt[0], "|")
	if len(f) < 3 {
		return id, true
	}
	asn := strings.Fields(strings.TrimSpace(f[0]))
	if len(asn) == 0 {
		return id, true
	}
	id.ASN = "AS" + asn[0]
	id.Country = strings.TrimSpace(f[2])
	if name, err := net.DefaultResolver.LookupTXT(c, "AS"+asn[0]+".asn.cymru.com"); err == nil && len(name) > 0 {
		nf := strings.Split(name[0], "|")
		if len(nf) >= 5 {
			if n := cymruName(nf[4]); n != "" {
				id.ASN += " " + n
			}
		}
	}
	return id, true
}

// cymruName turns Cymru's "CORBINA-AS - PJSC _Vimpelcom_, RU" into
// "PJSC Vimpelcom", the shape the HTTPS providers use. The asn field is a
// label people group by, so one operator must not appear under two
// spellings depending on which lookup path answered that day.
func cymruName(raw string) string {
	n := strings.TrimSpace(raw)
	if i := strings.Index(n, " - "); i >= 0 {
		n = n[i+3:]
	}
	if i := strings.LastIndex(n, ", "); i >= 0 && len(n)-i == 4 { // trailing ", CC"
		n = n[:i]
	}
	n = strings.ReplaceAll(n, "_", "")
	return strings.Join(strings.Fields(n), " ")
}

// viaHTTP asks the JSON providers.
func viaHTTP(ctx context.Context, timeout time.Duration) (Identity, bool) {
	client := &http.Client{Timeout: timeout}
	for _, s := range httpSources {
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
		id.Source = s.name
		return id, true
	}
	return Identity{}, false
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
// invalidates comparisons across the boundary. Only the number is compared:
// the two lookup paths spell operator names differently.
func Changed(prev, cur Identity) bool {
	return asNumber(prev.ASN) != "" && asNumber(cur.ASN) != "" && asNumber(prev.ASN) != asNumber(cur.ASN)
}

func asNumber(asn string) string {
	f := strings.Fields(asn)
	if len(f) == 0 {
		return ""
	}
	return strings.ToUpper(f[0])
}

// String renders the identity for logs.
func (i Identity) String() string {
	return fmt.Sprintf("%s / %s / %s", i.ASN, i.Country, i.Region)
}
