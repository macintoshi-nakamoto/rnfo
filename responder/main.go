// Command rnfo-responder is the controllable endpoint the probes measure
// against.
//
// Measuring third-party sites tells you that something was blocked. It cannot
// tell you what the block keyed on, because you control neither end. Against
// our own endpoint we control both, which makes three experiments possible
// that are otherwise impossible:
//
//   - Name versus address. The same address, reached with different server
//     names in the TLS ClientHello. If one name is reset and another is not,
//     the block is name-based; if the address dies for every name, it is
//     address-based. This is the whole reason the responder accepts any name.
//
//   - Volume triggers. /v1/bytes emits an exact number of bytes in chunks of a
//     chosen size with a chosen delay. If connections consistently die after
//     roughly the same amount of data, the amount is the trigger, and we can
//     measure where the edge is instead of guessing at it.
//
//   - Payload integrity. /v1/echo reports what the server actually received.
//     Comparing that with what the probe sent shows whether anything in the
//     path rewrote the traffic.
//
// Privacy: this service is reachable by anyone who finds it, so it logs in
// detail only connections that present the project token. Everything else is
// counted and discarded. No third-party traffic is recorded.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const version = "rnfo-responder/0.1.0"

type sniKey struct{}

var (
	token      string
	logMu      sync.Mutex
	logFile    *os.File
	anonCounts sync.Map // port -> *int64, connections we deliberately do not log
)

func main() {
	var (
		ports    = flag.String("ports", env("RNFO_PORTS", "443,8443,2053,9443"), "comma separated TLS ports")
		rawPorts = flag.String("raw-ports", env("RNFO_RAW_PORTS", ""), "comma separated plain TCP echo ports")
		certDir  = flag.String("cert-dir", env("RNFO_CERT_DIR", "/var/lib/rnfo-responder"), "where the certificate is kept")
		logPath  = flag.String("log", env("RNFO_RESPONDER_LOG", "/var/lib/rnfo-responder/connections.jsonl"), "connection log")
		names    = flag.String("names", env("RNFO_CERT_NAMES", "responder.invalid"), "comma separated names in the self-signed certificate")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}

	token = os.Getenv("RNFO_RESPONDER_TOKEN")
	if token == "" {
		log.Fatal("RNFO_RESPONDER_TOKEN is required: without it the responder would log strangers")
	}

	cert, fp, err := loadOrCreateCert(*certDir, strings.Split(*names, ","))
	if err != nil {
		log.Fatalf("certificate: %v", err)
	}
	log.Printf("%s starting; certificate sha256=%s", version, fp)

	if err := os.MkdirAll(filepath.Dir(*logPath), 0o750); err != nil {
		log.Fatalf("log dir: %v", err)
	}
	logFile, err = os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		log.Fatalf("log: %v", err)
	}
	defer logFile.Close()

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		// Any name is accepted and the same certificate is returned. The name
		// mismatch is intentional: the probe is not verifying identity here,
		// it is asking whether the name changed the network's behaviour.
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return cert, nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"` + version + `"}`))
	})
	mux.HandleFunc("/v1/echo", handleEcho)
	mux.HandleFunc("/v1/bytes", handleBytes)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 20 * time.Second,
		ErrorLog:          log.New(io_Discard{}, "", 0), // a torn-down connection is the measurement, not an error worth printing
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*tls.Conn); ok {
				return context.WithValue(ctx, sniKey{}, tc)
			}
			return ctx
		},
	}

	var wg sync.WaitGroup
	for _, p := range split(*ports) {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			ln, err := tls.Listen("tcp", ":"+p, tlsCfg)
			if err != nil {
				log.Printf("listen %s: %v", p, err)
				return
			}
			log.Printf("TLS listening on :%s", p)
			_ = srv.Serve(ln)
		}(p)
	}
	for _, p := range split(*rawPorts) {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			serveRaw(p)
		}(p)
	}
	wg.Wait()
}

// handleEcho reports what the server saw, so the probe can diff it against
// what it sent.
func handleEcho(w http.ResponseWriter, r *http.Request) {
	sni, ver, cipher, alpn := connInfo(r)
	authed := r.Header.Get("X-RNFO-Token") == token

	resp := map[string]any{
		"service":      version,
		"server_time":  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"sni":          sni,
		"tls_version":  ver,
		"tls_cipher":   cipher,
		"alpn":         alpn,
		"local_port":   localPort(r),
		"http_host":    r.Host,
		"http_proto":   r.Proto,
		"method":       r.Method,
		"path":         r.URL.RequestURI(),
		"user_agent":   r.UserAgent(),
		"header_count": len(r.Header),
		// A hash rather than the headers themselves: enough to prove nothing
		// was rewritten in flight, without storing anyone's request.
		"header_sha256": headerHash(r),
	}
	writeJSON(w, resp)
	logConn(r, authed, "echo", map[string]any{"sni": sni, "tls_version": ver, "alpn": alpn})
}

// handleBytes emits an exact volume of data under controlled pacing. The probe
// records how much actually arrived; the difference is the measurement.
//
//	n      total bytes to send      (default 65536, max 64 MiB)
//	chunk  bytes per write          (default 4096)
//	delay  milliseconds per chunk   (default 0)
func handleBytes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n := clamp(atoi(q.Get("n"), 64<<10), 1, 64<<20)
	chunk := clamp(atoi(q.Get("chunk"), 4096), 1, 1<<20)
	delay := time.Duration(clamp(atoi(q.Get("delay"), 0), 0, 10_000)) * time.Millisecond
	authed := r.Header.Get("X-RNFO-Token") == token

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(n))
	w.Header().Set("X-RNFO-Bytes", strconv.Itoa(n))
	w.WriteHeader(http.StatusOK)

	// A fixed, incompressible-looking but deterministic filler. Deterministic
	// so a probe can tell truncation from corruption; not random, so two runs
	// are comparable byte for byte.
	buf := make([]byte, chunk)
	for i := range buf {
		buf[i] = byte('A' + (i % 26))
	}
	flusher, _ := w.(http.Flusher)
	sent := 0
	for sent < n {
		k := chunk
		if n-sent < k {
			k = n - sent
		}
		if _, err := w.Write(buf[:k]); err != nil {
			logConn(r, authed, "bytes", map[string]any{"requested": n, "sent": sent, "chunk": chunk, "delay_ms": delay.Milliseconds(), "error": err.Error()})
			return
		}
		sent += k
		if flusher != nil {
			flusher.Flush()
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	logConn(r, authed, "bytes", map[string]any{"requested": n, "sent": sent, "chunk": chunk, "delay_ms": delay.Milliseconds()})
}

// serveRaw is a plain TCP endpoint that reports how many bytes it received
// before the connection ended. It exists for transports that are not HTTP.
func serveRaw(port string) {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Printf("raw listen %s: %v", port, err)
		return
	}
	log.Printf("raw TCP listening on :%s", port)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(60 * time.Second))
			buf := make([]byte, 32<<10)
			var total int64
			var first []byte
			for {
				n, err := c.Read(buf)
				if n > 0 {
					total += int64(n)
					if first == nil {
						first = append([]byte(nil), buf[:min(n, 64)]...)
					}
					_, _ = c.Write(buf[:n]) // echo, so the probe can measure the return path too
				}
				if err != nil {
					break
				}
			}
			// Only connections that open with the token are described; the
			// rest are a counter and nothing more.
			if len(first) > 0 && strings.HasPrefix(string(first), token) {
				writeLog(map[string]any{
					"ts": now(), "kind": "raw", "port": port, "bytes": total, "authed": true,
				})
			} else {
				bump(port)
			}
		}(c)
	}
}

// ------------------------------------------------------------------ logging

func logConn(r *http.Request, authed bool, kind string, extra map[string]any) {
	if !authed {
		// Someone who is not us connected. We record that it happened, on
		// which port, and nothing else. See the privacy note at the top.
		bump(localPort(r))
		return
	}
	rec := map[string]any{"ts": now(), "kind": kind, "authed": true, "port": localPort(r)}
	for k, v := range extra {
		rec[k] = v
	}
	writeLog(rec)
}

func writeLog(rec map[string]any) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	_, _ = logFile.Write(append(b, '\n'))
}

func bump(port string) {
	v, _ := anonCounts.LoadOrStore(port, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

// ------------------------------------------------------------------ helpers

func connInfo(r *http.Request) (sni, ver, cipher, alpn string) {
	tc, _ := r.Context().Value(sniKey{}).(*tls.Conn)
	if tc == nil {
		return "", "", "", ""
	}
	cs := tc.ConnectionState()
	switch cs.Version {
	case tls.VersionTLS13:
		ver = "TLS1.3"
	case tls.VersionTLS12:
		ver = "TLS1.2"
	default:
		ver = fmt.Sprintf("0x%04x", cs.Version)
	}
	return cs.ServerName, ver, tls.CipherSuiteName(cs.CipherSuite), cs.NegotiatedProtocol
}

func localPort(r *http.Request) string {
	tc, _ := r.Context().Value(sniKey{}).(*tls.Conn)
	if tc == nil {
		return ""
	}
	_, p, _ := net.SplitHostPort(tc.LocalAddr().String())
	return p
}

func headerHash(r *http.Request) string {
	h := sha256.New()
	for _, k := range []string{"User-Agent", "Accept", "Accept-Language", "Accept-Encoding", "Connection"} {
		h.Write([]byte(k + ":" + r.Header.Get(k) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

// loadOrCreateCert keeps one long-lived self-signed certificate. Its
// fingerprint goes into the documentation: a probe that sees a different
// fingerprint is not talking to us.
func loadOrCreateCert(dir string, names []string) (*tls.Certificate, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", err
	}
	certPath := filepath.Join(dir, "responder.crt")
	keyPath := filepath.Join(dir, "responder.key")

	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		sum := sha256.Sum256(c.Certificate[0])
		return &c, hex.EncodeToString(sum[:]), nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: strings.TrimSpace(names[0]), Organization: []string{"RNFO measurement responder"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return nil, "", err
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(c.Certificate[0])
	return &c, hex.EncodeToString(sum[:]), nil
}

type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoi(s string, d int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return d
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
