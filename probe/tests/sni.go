package tests

import (
	"context"
	"math/rand"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
)

// The SNI experiment.
//
// Tier 1 can observe that a connection to bbc.com dies during the TLS
// handshake. It cannot say whether the network reacted to the *name* bbc.com
// or to the *address* bbc.com resolves to, because we control neither.
//
// Against our own responder we control both. Every trial below opens a
// connection to the same address, on the same port, differing only in the
// server name carried in the ClientHello. If names that Tier 1 saw blocked are
// reset here and neutral names are not, the trigger is the name, and the
// address had nothing to do with it. If every name survives, the blocking we
// observed was address-based.
//
// The empty-SNI trial is the baseline: it is the same connection with no name
// at all, so anything that kills it was reacting to the address.
//
// Rule on which names may be used: only names already present in the pinned
// target lists, plus our own. No packet ever reaches those hosts - the name is
// a string in our own ClientHello to our own server - but keeping the set
// inside the lists means the experiment cannot quietly widen what this project
// touches.

// SNITrial is one name to put in the ClientHello.
type SNITrial struct {
	Name  string // empty means: send no SNI at all
	Class string // own | none | neutral | blocked_observed
}

// DefaultSNITrials is the standard set. The "blocked_observed" names are ones
// this project measured being reset at the TLS stage from Moscow on
// 2026-09-04; the neutral ones completed normally in the same run.
var DefaultSNITrials = []SNITrial{
	{"rnfo-responder.invalid", "own"},
	{"", "none"},
	{"example.com", "neutral"},
	{"www.debian.org", "neutral"},
	{"www.kernel.org", "neutral"},
	{"www.bbc.com", "blocked_observed"},
	{"www.hrw.org", "blocked_observed"},
	{"censortracker.org", "blocked_observed"},
	{"www.ned.org", "blocked_observed"},
	{"www.euronews.com", "blocked_observed"},
}

// SNIExperiment runs every trial repeats times against ip:port.
//
// Trials are interleaved rather than grouped: all names are tried once, then
// again, and so on. Grouping would confound the name with the moment it was
// measured, which matters because filtering state can change between the first
// trial and the last, and because a block triggered by one name may persist
// for the whole tuple afterwards.
func SNIExperiment(ctx context.Context, ip, port string, repeats int, trials []SNITrial, o Options) []*schema.Measurement {
	if repeats < 1 {
		repeats = 1
	}
	var out []*schema.Measurement
	for round := 1; round <= repeats; round++ {
		order := make([]SNITrial, len(trials))
		copy(order, trials)
		rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] }) //nolint:gosec
		for _, t := range order {
			if ctx.Err() != nil {
				return out
			}
			opt := o
			opt.ForceIP = ip
			host := t.Name
			if host == "" {
				// No name to send. The URL still needs a host, so the address
				// is used, and an address is never sent as SNI.
				host = ip
				opt.SNI = ""
			} else {
				opt.SNI = t.Name
			}
			url := "https://" + host
			if port != "443" {
				url += ":" + port
			}
			url += "/v1/echo"

			m := Reach(ctx, url, opt)
			m.List = "sni"
			m.Category = t.Class
			m.Target = t.Name
			if m.Target == "" {
				m.Target = "(no sni)"
			}
			m.Attempt = round
			out = append(out, m)

			// Spacing matters here. Back-to-back connections to one tuple can
			// hit residual blocking left by the previous trial, which would be
			// read as the current name being blocked.
			select {
			case <-ctx.Done():
				return out
			case <-time.After(time.Duration(1500+rand.Intn(1000)) * time.Millisecond): //nolint:gosec
			}
		}
	}
	return out
}
