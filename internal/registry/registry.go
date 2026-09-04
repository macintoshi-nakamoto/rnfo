// Package registry loads probes.yaml, the authority on what a probe id means.
package registry

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Probe is one vantage point. The fields mirror probes.yaml; see the comments
// there for what each one is for.
type Probe struct {
	ID        string `yaml:"id"`
	Role      string `yaml:"role"`
	Status    string `yaml:"status"`
	Since     string `yaml:"since"`
	Net       string `yaml:"net"`
	Country   string `yaml:"country"`
	Region    string `yaml:"region"`
	City      string `yaml:"city"`
	ASN       string `yaml:"asn"`
	ASNName   string `yaml:"asn_name"`
	Operator  string `yaml:"operator"`
	Uplink    string `yaml:"uplink"`
	Consent   string `yaml:"consent"`
	Dedicated bool   `yaml:"dedicated"`
	Clock     string `yaml:"clock"`
	Notes     string `yaml:"notes"`
}

// Registry is the parsed probes.yaml.
type Registry struct {
	Schema string  `yaml:"schema"`
	Probes []Probe `yaml:"probes"`

	byID map[string]*Probe
}

// Load reads and checks the registry. A registry that does not itself validate
// cannot be used to validate anything else, so the checks here are strict.
func Load(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var r Registry
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Schema != "v1" {
		return nil, fmt.Errorf("%s: unknown registry schema %q", path, r.Schema)
	}
	r.byID = make(map[string]*Probe, len(r.Probes))
	for i := range r.Probes {
		p := &r.Probes[i]
		if p.ID == "" {
			return nil, fmt.Errorf("%s: probe %d has no id", path, i)
		}
		if _, dup := r.byID[p.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate probe id %q", path, p.ID)
		}
		switch p.Status {
		case "active", "planned", "retired":
		default:
			return nil, fmt.Errorf("%s: probe %s has unknown status %q", path, p.ID, p.Status)
		}
		switch p.Role {
		case "probe", "control", "responder", "collector":
		default:
			return nil, fmt.Errorf("%s: probe %s has unknown role %q", path, p.ID, p.Role)
		}
		if p.Status == "active" {
			switch p.Net {
			case "hosting", "eyeball", "mobile":
			default:
				return nil, fmt.Errorf("%s: active probe %s has unknown net %q", path, p.ID, p.Net)
			}
			// Consent is not optional for an active probe. A vantage point
			// running on someone else's machine without recorded consent
			// cannot be in a published dataset, so it cannot be active here.
			switch p.Consent {
			case "owner", "recorded":
			default:
				return nil, fmt.Errorf("%s: active probe %s has no recorded consent (%q)", path, p.ID, p.Consent)
			}
		}
		r.byID[p.ID] = p
	}
	return &r, nil
}

// Known reports whether id is a registered probe.
func (r *Registry) Known(id string) bool { _, ok := r.byID[id]; return ok }

// Get returns the registry entry for id, or nil.
func (r *Registry) Get(id string) *Probe { return r.byID[id] }

// Active returns the probes currently expected to be producing data.
func (r *Registry) Active() []Probe {
	var out []Probe
	for _, p := range r.Probes {
		if p.Status == "active" {
			out = append(out, p)
		}
	}
	return out
}
