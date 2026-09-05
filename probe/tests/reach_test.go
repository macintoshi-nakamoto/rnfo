package tests

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/macintoshi-nakamoto/rnfo/internal/classify"
	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
)

func quick() Options {
	o := DefaultOptions()
	o.ConnectTimeout = 2 * time.Second
	o.TLSTimeout = 2 * time.Second
	o.TotalTimeout = 3 * time.Second
	o.Family = "auto"
	return o
}

func TestReachOK(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "unit-test")
		_, _ = w.Write([]byte("<html><head><title>  Hello\n  world </title></head><body>" + strings.Repeat("x", 1000) + "</body></html>"))
	}))
	defer srv.Close()

	m := Reach(context.Background(), srv.URL, quick())
	if m.Verdict != classify.VOK || m.Stage != schema.StageOK {
		t.Fatalf("verdict %s stage %s err %q", m.Verdict, m.Stage, m.Err)
	}
	if m.HTTP != 200 || m.CurlRC != 0 || m.Server != "unit-test" {
		t.Errorf("http=%d curl_rc=%d server=%q", m.HTTP, m.CurlRC, m.Server)
	}
	if m.Title != "Hello world" {
		t.Errorf("title collapsed whitespace wrong: %q", m.Title)
	}
	if m.BodyLen < 1000 || len(m.BodySHA256) != 64 || m.BytesRead == 0 {
		t.Errorf("body_len=%d sha=%q bytes_read=%d", m.BodyLen, m.BodySHA256, m.BytesRead)
	}
	if m.TLSVersion == "" || m.CertSHA256 == "" || m.IPFamily != "v4" {
		t.Errorf("tls=%q cert=%q family=%q", m.TLSVersion, m.CertSHA256, m.IPFamily)
	}
	if m.TConnect <= 0 || m.TTLS <= 0 || m.TTotal <= 0 {
		t.Errorf("timings not recorded: connect=%v tls=%v total=%v", m.TConnect, m.TTLS, m.TTotal)
	}
	// httptest's certificate is for example.com and 127.0.0.1; the request
	// went to 127.0.0.1, so the name check should pass.
	if m.CertNameOK == nil || !*m.CertNameOK {
		t.Errorf("cert_name_ok should be true for the test server, got %v", m.CertNameOK)
	}
}

func TestReachConnectRefused(t *testing.T) {
	// Grab a free port and close it again, so nothing listens there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	m := Reach(context.Background(), "https://"+addr+"/", quick())
	if m.Stage != schema.StageTCP || m.Verdict != classify.VConnRefused {
		t.Errorf("stage %s verdict %s (err %q)", m.Stage, m.Verdict, m.Err)
	}
	if m.CurlRC != 7 || m.BytesRead != 0 {
		t.Errorf("curl_rc=%d bytes_read=%d", m.CurlRC, m.BytesRead)
	}
}

// A listener that accepts and never speaks looks, from the outside, like a
// path that silently drops the ClientHello: the TLS stage times out.
func TestReachTLSTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { time.Sleep(10 * time.Second); c.Close() }(c)
		}
	}()
	o := quick()
	o.TLSTimeout = 500 * time.Millisecond
	m := Reach(context.Background(), "https://"+l.Addr().String()+"/", o)
	if m.Stage != schema.StageTLS || m.Verdict != classify.VTLSTimeout {
		t.Errorf("stage %s verdict %s (err %q)", m.Stage, m.Verdict, m.Err)
	}
	if m.CurlRC != 35 {
		t.Errorf("curl_rc=%d, want 35", m.CurlRC)
	}
}

// A server that closes the socket the moment it sees the ClientHello is what
// an injected reset looks like at this layer. Go reports it as EOF or reset
// depending on timing; both are TLS-stage failures and neither is a timeout.
func TestReachTLSCutAfterClientHello(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 64)
				_, _ = c.Read(buf) // wait for the ClientHello
				if tc, ok := c.(*net.TCPConn); ok {
					_ = tc.SetLinger(0) // RST instead of FIN
				}
				c.Close()
			}(c)
		}
	}()
	m := Reach(context.Background(), "https://"+l.Addr().String()+"/", quick())
	if m.Stage != schema.StageTLS {
		t.Errorf("stage %s, want tls (verdict %s err %q)", m.Stage, m.Verdict, m.Err)
	}
	if m.Verdict != classify.VTLSReset && m.Verdict != classify.VTLSFail {
		t.Errorf("verdict %s, want tls_reset or tls_fail (err %q)", m.Verdict, m.Err)
	}
	if m.Verdict == classify.VTLSTimeout {
		t.Error("a cut connection must not be classified as a timeout")
	}
}

func TestReachNoAddressFamily(t *testing.T) {
	o := quick()
	o.Family = "v6"
	m := Reach(context.Background(), "https://127.0.0.1:1/", o)
	if m.Verdict != classify.VNoAddrFamily || m.Stage != schema.StageDNS {
		t.Errorf("verdict %s stage %s", m.Verdict, m.Stage)
	}
	if m.CurlRC != 6 {
		t.Errorf("curl_rc=%d, want 6", m.CurlRC)
	}
}

// The body cap must be honoured and reported, because on a metered SIM the
// cap is small and body_len must be read against it.
func TestReachBodyCap(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("y", 300*1024)))
	}))
	defer srv.Close()
	o := quick()
	o.MaxBody = 128 * 1024
	m := Reach(context.Background(), srv.URL, o)
	if m.Verdict != classify.VOK {
		t.Fatalf("verdict %s err %q", m.Verdict, m.Err)
	}
	if !m.BodyTruncated || m.BodyLen < 128*1024 || m.BodyLen > 128*1024+64*1024 {
		t.Errorf("truncated=%v body_len=%d for a 128 KiB cap", m.BodyTruncated, m.BodyLen)
	}
}

func TestReachMalformedURL(t *testing.T) {
	m := Reach(context.Background(), "::not a url::", quick())
	if m.Verdict != classify.VOther || m.Stage != schema.StageDNS {
		t.Errorf("verdict %s stage %s", m.Verdict, m.Stage)
	}
}
