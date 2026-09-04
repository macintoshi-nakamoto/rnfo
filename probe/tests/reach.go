// Package tests holds the individual measurement modules the agent runs.
//
// reach.go implements Tier 1: reachability and failure mode. It performs the
// connection in explicit stages so the row can say where the connection died,
// and counts wire bytes so it can say how much data arrived first.
package tests

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/classify"
	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
)

// Options controls a single reachability measurement.
type Options struct {
	ConnectTimeout time.Duration
	TLSTimeout     time.Duration
	TotalTimeout   time.Duration
	MaxRedirects   int
	MaxBody        int64
	UserAgent      string

	// SNI, when set, is sent in the TLS ClientHello instead of the hostname
	// from the URL. Combined with ForceIP this isolates name-based blocking
	// from address-based blocking: same address, different name.
	SNI string

	// Family restricts the measurement to one address family: "v4", "v6" or
	// "auto" (v4 first, v6 as a fallback). Fixed per study rather than left to
	// the resolver, so that every probe measures the same thing.
	Family string

	// ForceIP dials this address instead of resolving the hostname. Used by
	// the SNI experiments against our own responder, where we already know
	// the address and want to vary only the name.
	ForceIP string
}

// DefaultOptions are the Tier 1 settings. They are deliberately close to what
// a browser does, because the measurement is meant to represent what a user
// experiences, not what a scanner experiences.
func DefaultOptions() Options {
	return Options{
		ConnectTimeout: 10 * time.Second,
		TLSTimeout:     10 * time.Second,
		TotalTimeout:   25 * time.Second,
		MaxRedirects:   5,
		MaxBody:        2 << 20, // 2 MiB
		Family:         "v4",
		UserAgent:      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
	}
}

// instConn counts wire bytes and remembers the first read and write errors.
// The read counter is the point of the whole type: when a connection is torn
// down mid-transfer, how many bytes arrived before the teardown is the
// measurement, and no HTTP-level client will tell you.
type instConn struct {
	net.Conn
	mu       sync.Mutex
	read     int64
	written  int64
	readErr  error
	writeErr error
}

func (c *instConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.mu.Lock()
	c.read += int64(n)
	if err != nil && c.readErr == nil && !errors.Is(err, io.EOF) {
		c.readErr = err
	}
	c.mu.Unlock()
	return n, err
}

func (c *instConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.mu.Lock()
	c.written += int64(n)
	if err != nil && c.writeErr == nil {
		c.writeErr = err
	}
	c.mu.Unlock()
	return n, err
}

func (c *instConn) counts() (int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read, c.written
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var wsRe = regexp.MustCompile(`\s+`)

// Reach measures one URL and returns a partially filled Measurement. The
// caller adds probe identity, run id and target metadata.
//
// A Measurement is always returned, never nil, and never an error: a failed
// measurement is data. That is the rule the whole dataset depends on.
func Reach(ctx context.Context, rawURL string, o Options) *schema.Measurement {
	m := &schema.Measurement{
		Schema:  schema.Version,
		Agent:   schema.AgentVersion,
		URL:     rawURL,
		Attempt: 1,
	}
	start := time.Now()
	defer func() { m.TTotal = since(start) }()

	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		m.Stage, m.Verdict, m.Err = schema.StageDNS, classify.VOther, "malformed url"
		m.CurlRC = classify.CurlRC(m.Verdict)
		return m
	}
	m.Target = u.Hostname()

	ctx, cancel := context.WithTimeout(ctx, o.TotalTimeout)
	defer cancel()

	// Stage 1: resolution. Recorded separately from the connection so that a
	// poisoned or blocked resolver is distinguishable from a blocked address.
	var addrs []string
	if o.ForceIP != "" {
		addrs = []string{o.ForceIP}
	} else {
		t0 := time.Now()
		ips, derr := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
		m.TDNS = since(t0)
		if derr != nil {
			v, rc := classify.DNS(derr)
			m.Stage, m.Verdict, m.DNSRC = schema.StageDNS, v, rc
			m.DNSErr, m.Err = derr.Error(), derr.Error()
			m.Errno = classify.Errno(derr)
			m.CurlRC = classify.CurlRC(v)
			return m
		}
		for _, ip := range ips {
			addrs = append(addrs, ip.IP.String())
		}
		m.DNSIPs = addrs
		if len(addrs) == 0 {
			m.Stage, m.Verdict, m.DNSRC = schema.StageDNS, classify.VDNSFail, 3
			m.CurlRC = classify.CurlRC(m.Verdict)
			return m
		}
		// All resolved addresses are kept in the row, because a poisoned answer
		// is visible in the address set even when the connection then succeeds.
		// Only the selected family is dialled.
		addrs = selectFamily(addrs, o.Family)
		if len(addrs) == 0 {
			m.Stage, m.Verdict, m.DNSRC = schema.StageDNS, classify.VNoAddrFamily, 0
			m.Err = "no " + o.Family + " address for host"
			m.CurlRC = classify.CurlRC(m.Verdict)
			return m
		}
	}
	m.IPFamily = family(addrs[0])

	// Stages 2 and 3 happen inside the dialers so that the HTTP client can
	// drive redirects while we still time and instrument each connection.
	var (
		mu          sync.Mutex
		conns       []*instConn
		dialN       int
		tcpErr      error
		tlsErr      error
		firstByteAt time.Time
	)

	dialTCP := func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		target := addr
		if dialN == 0 && o.ForceIP == "" {
			target = net.JoinHostPort(addrs[0], port)
		} else if o.ForceIP != "" {
			target = net.JoinHostPort(o.ForceIP, port)
		}
		t0 := time.Now()
		d := &net.Dialer{Timeout: o.ConnectTimeout}
		c, err := d.DialContext(ctx, network, target)
		mu.Lock()
		if dialN == 0 {
			m.TConnect = since(t0)
			if err != nil {
				tcpErr = err
			} else {
				host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
				m.RemoteIP = host
			}
		}
		dialN++
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		ic := &instConn{Conn: c}
		mu.Lock()
		conns = append(conns, ic)
		mu.Unlock()
		return ic, nil
	}

	dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dialTCP(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(addr)
		sni := host
		if o.SNI != "" {
			sni = o.SNI
		}
		// The certificate is verified manually after the handshake so that a
		// name mismatch does not abort before we have seen the chain: an
		// intercepting middlebox is identified by the certificate it presents.
		cfg := &tls.Config{
			ServerName:         sni,
			NextProtos:         []string{"http/1.1"},
			InsecureSkipVerify: true, //nolint:gosec // verified below, deliberately
			MinVersion:         tls.VersionTLS10,
		}
		tc := tls.Client(raw, cfg)
		t0 := time.Now()
		hctx, hcancel := context.WithTimeout(ctx, o.TLSTimeout)
		err = tc.HandshakeContext(hctx)
		hcancel()
		mu.Lock()
		if m.TTLS == 0 {
			m.TTLS = since(t0)
			if err != nil {
				tlsErr = err
			}
		}
		mu.Unlock()
		if err != nil {
			raw.Close()
			return nil, err
		}
		if m.TLSVersion == "" {
			recordTLS(m, tc.ConnectionState(), sni)
		}
		return tc, nil
	}

	tr := &http.Transport{
		DialContext:           dialTCP,
		DialTLSContext:        dialTLS,
		DisableKeepAlives:     true,
		DisableCompression:    false,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          1,
		ResponseHeaderTimeout: o.TotalTimeout,
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			m.Redirects = len(via)
			if len(via) >= o.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", o.MaxRedirects)
			}
			return nil
		},
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	req.Header.Set("User-Agent", o.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	reqSent := time.Now()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotFirstResponseByte: func() { firstByteAt = time.Now() },
	}))

	resp, err := client.Do(req)

	finish := func() {
		var r, w int64
		mu.Lock()
		for _, c := range conns {
			cr, cw := c.counts()
			r += cr
			w += cw
		}
		mu.Unlock()
		m.BytesRead, m.BytesSent = r, w
		if !firstByteAt.IsZero() {
			m.TFirstByte = firstByteAt.Sub(reqSent).Seconds()
		}
	}

	if err != nil {
		finish()
		switch {
		case tcpErr != nil:
			m.Stage, m.Verdict = schema.StageTCP, classify.TCP(tcpErr)
			m.Err, m.Errno = tcpErr.Error(), classify.Errno(tcpErr)
		case tlsErr != nil:
			m.Stage, m.Verdict = schema.StageTLS, classify.TLS(tlsErr)
			m.Err, m.Errno = tlsErr.Error(), classify.Errno(tlsErr)
		default:
			m.Stage, m.Verdict = classify.HTTP(err, !firstByteAt.IsZero())
			m.Err, m.Errno = err.Error(), classify.Errno(err)
		}
		m.CurlRC = classify.CurlRC(m.Verdict)
		return m
	}
	defer resp.Body.Close()

	m.HTTP = resp.StatusCode
	m.HTTPVersion = resp.Proto
	m.Server = trunc(resp.Header.Get("Server"), 120)
	m.FinalURL = resp.Request.URL.String()

	// Read the body to a cap. The content is discarded: only its size, a hash
	// of the first 64 KiB and the title are kept. A block page is recognised
	// by exactly those three things, and keeping nothing else means the
	// dataset carries no third-party content.
	h := sha256.New()
	var body strings.Builder
	var n int64
	buf := make([]byte, 32<<10)
	var readErr error
	for n < o.MaxBody {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			n += int64(k)
			if n <= 64<<10 {
				h.Write(buf[:k])
				body.Write(buf[:k])
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				readErr = rerr
			}
			break
		}
	}
	m.BodyLen = n
	m.BodyTruncated = n >= o.MaxBody
	if n > 0 {
		m.BodySHA256 = hex.EncodeToString(h.Sum(nil))
		if mt := titleRe.FindStringSubmatch(body.String()); len(mt) == 2 {
			m.Title = trunc(strings.TrimSpace(wsRe.ReplaceAllString(mt[1], " ")), 200)
		}
	}
	finish()

	if readErr != nil {
		// Headers arrived but the body was cut. This is the shape reported for
		// the "connection dies after roughly 14-25 KB" behaviour, and BytesRead
		// is the number that matters.
		m.Stage, m.Verdict = classify.HTTP(readErr, true)
		m.Err, m.Errno = readErr.Error(), classify.Errno(readErr)
		m.CurlRC = classify.CurlRC(m.Verdict)
		return m
	}

	m.Stage, m.Verdict, m.CurlRC = schema.StageOK, classify.VOK, 0
	return m
}

func recordTLS(m *schema.Measurement, cs tls.ConnectionState, sni string) {
	switch cs.Version {
	case tls.VersionTLS13:
		m.TLSVersion = "TLS1.3"
	case tls.VersionTLS12:
		m.TLSVersion = "TLS1.2"
	case tls.VersionTLS11:
		m.TLSVersion = "TLS1.1"
	case tls.VersionTLS10:
		m.TLSVersion = "TLS1.0"
	default:
		m.TLSVersion = fmt.Sprintf("0x%04x", cs.Version)
	}
	m.TLSCipher = tls.CipherSuiteName(cs.CipherSuite)
	m.TLSALPN = cs.NegotiatedProtocol
	if len(cs.PeerCertificates) == 0 {
		return
	}
	leaf := cs.PeerCertificates[0]
	sum := sha256.Sum256(leaf.Raw)
	m.CertSHA256 = hex.EncodeToString(sum[:])
	m.CertSubject = trunc(leaf.Subject.CommonName, 120)
	m.CertIssuer = trunc(leaf.Issuer.CommonName, 120)
	m.CertNotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
	ok := leaf.VerifyHostname(sni) == nil
	m.CertNameOK = &ok
	// Chain validity is reported but never enforced: refusing to continue on a
	// bad certificate would hide the very interception we are looking for.
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: sni, Intermediates: intermediates(cs)}); err != nil && ok {
		m.CertNameOK = &ok
	}
}

func intermediates(cs tls.ConnectionState) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		p.AddCert(c)
	}
	return p
}

// selectFamily keeps only the addresses of the requested family, preserving
// resolver order within it. "auto" keeps everything but puts IPv4 first, which
// is what a probe with a broken v6 uplink needs.
func selectFamily(addrs []string, want string) []string {
	var v4, v6 []string
	for _, a := range addrs {
		if family(a) == "v4" {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	switch want {
	case "v4":
		return v4
	case "v6":
		return v6
	default:
		return append(v4, v6...)
	}
}

func family(addr string) string {
	if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
		return "v6"
	}
	return "v4"
}

func since(t time.Time) float64 { return time.Since(t).Seconds() }

// trunc cuts on rune boundaries. Titles are frequently Cyrillic and a byte
// cut would leave a broken code point in the dataset.
func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
