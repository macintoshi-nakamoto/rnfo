package classify

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
)

// The verdict vocabulary is closed. Every verdict must map to a curl code and
// every curl code must be one the documentation lists, because a row that
// carries a verdict the validator does not know is rejected on arrival.
func TestEveryVerdictHasACurlCode(t *testing.T) {
	documented := map[int]bool{0: true, 2: true, 6: true, 7: true, 28: true, 35: true, 47: true, 52: true, 56: true, 60: true}
	for v, rc := range curlRC {
		if !Valid(v) {
			t.Errorf("verdict %q is in the table but Valid() says no", v)
		}
		if !documented[rc] {
			t.Errorf("verdict %q maps to curl code %d, which docs/SCHEMA.md does not list", v, rc)
		}
	}
	if Valid("made_up") {
		t.Error("an unknown verdict must not be valid")
	}
	if CurlRC("made_up") != 2 {
		t.Error("an unknown verdict must fall back to curl code 2")
	}
}

func opErr(err error) error {
	return &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", err)}
}

func TestErrno(t *testing.T) {
	cases := map[string]error{
		"ECONNRESET":   opErr(syscall.ECONNRESET),
		"ECONNREFUSED": opErr(syscall.ECONNREFUSED),
		"ETIMEDOUT":    opErr(syscall.ETIMEDOUT),
		"EHOSTUNREACH": opErr(syscall.EHOSTUNREACH),
		"":             errors.New("something else entirely"),
	}
	for want, err := range cases {
		if got := Errno(err); got != want {
			t.Errorf("Errno(%v) = %q, want %q", err, got, want)
		}
	}
	// The Windows text form must classify too, because the home probe was
	// once going to be a Windows machine and the string is what Go gives there.
	if Errno(errors.New("wsarecv: An existing connection was forcibly closed by the remote host.")) != "ECONNRESET" {
		t.Error("Windows reset text not recognised")
	}
}

func TestTCP(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, VConnTimeout},
		{opErr(syscall.ECONNRESET), VConnReset},
		{opErr(syscall.ECONNREFUSED), VConnRefused},
		{opErr(syscall.EHOSTUNREACH), VConnUnreachable},
		{opErr(syscall.ENETUNREACH), VConnUnreachable},
		{errors.New("weird"), VOther},
	}
	for _, c := range cases {
		if got := TCP(c.err); got != c.want {
			t.Errorf("TCP(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}

func TestTLS(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{x509.HostnameError{Host: "a"}, VTLSCertName},
		{x509.UnknownAuthorityError{}, VTLSCertInvalid},
		{x509.CertificateInvalidError{}, VTLSCertInvalid},
		{context.DeadlineExceeded, VTLSTimeout},
		{opErr(syscall.ECONNRESET), VTLSReset},
		{errors.New("tls: handshake failure"), VTLSFail},
	}
	for _, c := range cases {
		err := c.err
		// HostnameError is matched by pointer in classify, as errors.As
		// receives it from crypto/tls.
		if he, ok := err.(x509.HostnameError); ok {
			err = &he
		}
		if got := TLS(err); got != c.want {
			t.Errorf("TLS(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}

// Stage matters more than mechanism here: the same reset means a different
// thing before and after the first response byte arrived.
func TestHTTPStageAndVerdict(t *testing.T) {
	cases := []struct {
		err         error
		gotResponse bool
		stage       string
		verdict     string
	}{
		{opErr(syscall.ECONNRESET), false, schema.StageRequest, VReqReset},
		{opErr(syscall.ECONNRESET), true, schema.StageResponse, VRespReset},
		{context.DeadlineExceeded, false, schema.StageRequest, VReqTimeout},
		{context.DeadlineExceeded, true, schema.StageResponse, VRespTimeout},
		{io.ErrUnexpectedEOF, true, schema.StageResponse, VRespTruncated},
		{io.ErrUnexpectedEOF, false, schema.StageRequest, VRespEmpty},
		{errors.New("Get \"x\": stopped after 5 redirects"), false, schema.StageRequest, VTooManyRedirect},
	}
	for _, c := range cases {
		stage, verdict := HTTP(c.err, c.gotResponse)
		if stage != c.stage || verdict != c.verdict {
			t.Errorf("HTTP(%v, %v) = %s/%s, want %s/%s", c.err, c.gotResponse, stage, verdict, c.stage, c.verdict)
		}
	}
}

func TestDNS(t *testing.T) {
	if v, rc := DNS(nil); v != VOK || rc != 0 {
		t.Errorf("DNS(nil) = %s/%d", v, rc)
	}
	if v, rc := DNS(&net.DNSError{IsNotFound: true}); v != VDNSNXDomain || rc != 1 {
		t.Errorf("nxdomain = %s/%d", v, rc)
	}
	if v, rc := DNS(&net.DNSError{IsTimeout: true}); v != VDNSTimeout || rc != 2 {
		t.Errorf("timeout = %s/%d", v, rc)
	}
	if v, rc := DNS(errors.New("servfail")); v != VDNSFail || rc != 3 {
		t.Errorf("other = %s/%d", v, rc)
	}
}
