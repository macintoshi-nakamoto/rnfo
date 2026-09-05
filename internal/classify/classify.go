// Package classify turns a Go network error into the failure taxonomy the
// dataset is built on.
//
// Why not just shell out to curl and keep its exit code? Because curl reports
// what went wrong but not when. Exit code 56 ("connection reset by peer") is
// returned whether the reset arrived immediately after the TCP handshake, right
// after the TLS ClientHello carrying the server name, or in the middle of the
// response body. Those three cases are three different filtering mechanisms.
// We classify by stage and keep the curl code as a compatibility field so this
// dataset stays comparable with curl-based measurements published elsewhere.
package classify

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/macintoshi-nakamoto/rnfo/internal/schema"
)

// Verdicts. Kept as a closed set: the validator rejects anything else, so a
// new failure mode cannot silently enter the dataset as free text.
const (
	VOK              = "ok"
	VDNSNXDomain     = "dns_nxdomain"
	VDNSTimeout      = "dns_timeout"
	VDNSFail         = "dns_fail"
	VNoAddrFamily    = "no_address_family"
	VConnRefused     = "connect_refused"
	VConnTimeout     = "connect_timeout"
	VConnUnreachable = "connect_unreachable"
	VConnReset       = "connect_reset"
	VTLSReset        = "tls_reset"
	VTLSTimeout      = "tls_timeout"
	VTLSCertName     = "tls_cert_name"
	VTLSCertInvalid  = "tls_cert_invalid"
	VTLSFail         = "tls_fail"
	VReqReset        = "request_reset"
	VReqTimeout      = "request_timeout"
	VRespReset       = "response_reset"
	VRespTimeout     = "response_timeout"
	VRespTruncated   = "response_truncated"
	VRespEmpty       = "response_empty"
	VTooManyRedirect = "too_many_redirects"
	VOther           = "other"
)

// Valid reports whether v is a verdict this schema version knows about.
func Valid(v string) bool { _, ok := curlRC[v]; return ok }

// curlRC maps our verdicts onto the curl exit codes a reader already knows, so the
// curl_rc column keeps its documented meaning:
//
//	6  could not resolve host
//	7  failed to connect
//	28 operation timed out
//	35 TLS connect error
//	52 empty reply from server
//	56 failure receiving network data (reset)
//	60 peer certificate cannot be authenticated
//	47 too many redirects
var curlRC = map[string]int{
	VOK:              0,
	VDNSNXDomain:     6,
	VDNSTimeout:      6,
	VDNSFail:         6,
	VNoAddrFamily:    6,
	VConnRefused:     7,
	VConnUnreachable: 7,
	VConnTimeout:     28,
	VConnReset:       56,
	VTLSReset:        35,
	VTLSTimeout:      35,
	VTLSCertName:     60,
	VTLSCertInvalid:  60,
	VTLSFail:         35,
	VReqReset:        56,
	VReqTimeout:      28,
	VRespReset:       56,
	VRespTimeout:     28,
	VRespTruncated:   56,
	VRespEmpty:       52,
	VTooManyRedirect: 47,
	VOther:           2,
}

// CurlRC returns the compatibility exit code for a verdict.
func CurlRC(verdict string) int {
	if rc, ok := curlRC[verdict]; ok {
		return rc
	}
	return 2
}

// Errno extracts the underlying kernel error name, which is the ground truth
// about what the network did to us. An injected RST and a genuine one are
// indistinguishable here; separating them is the job of the TTL analysis in
// Tier 2, not of this function.
func Errno(err error) string {
	if err == nil {
		return ""
	}
	var se syscall.Errno
	if errors.As(err, &se) {
		switch {
		case errors.Is(se, syscall.ECONNRESET):
			return "ECONNRESET"
		case errors.Is(se, syscall.ECONNREFUSED):
			return "ECONNREFUSED"
		case errors.Is(se, syscall.ETIMEDOUT):
			return "ETIMEDOUT"
		case errors.Is(se, syscall.EHOSTUNREACH):
			return "EHOSTUNREACH"
		case errors.Is(se, syscall.ENETUNREACH):
			return "ENETUNREACH"
		case errors.Is(se, syscall.EPIPE):
			return "EPIPE"
		}
		// A syscall.Errno that is none of the above. On Windows this is the
		// normal case: WSAECONNREFUSED and friends are Errno values that do
		// not compare equal to the Unix constants, so fall through to the
		// text of the error rather than returning an opaque number.
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "reset by peer"), strings.Contains(s, "forcibly closed"):
		return "ECONNRESET"
	case strings.Contains(s, "refused"):
		return "ECONNREFUSED"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "unreachable"):
		return "EHOSTUNREACH"
	}
	return ""
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isReset(err error) bool { return Errno(err) == "ECONNRESET" || Errno(err) == "EPIPE" }

// DNS classifies a resolver failure.
func DNS(err error) (verdict string, rc int) {
	if err == nil {
		return VOK, 0
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		switch {
		case de.IsNotFound:
			return VDNSNXDomain, 1
		case de.IsTimeout:
			return VDNSTimeout, 2
		}
	}
	if isTimeout(err) {
		return VDNSTimeout, 2
	}
	return VDNSFail, 3
}

// TCP classifies a connect failure.
func TCP(err error) string {
	switch {
	case isTimeout(err):
		return VConnTimeout
	case isReset(err):
		return VConnReset
	}
	switch Errno(err) {
	case "ECONNREFUSED":
		return VConnRefused
	case "EHOSTUNREACH", "ENETUNREACH":
		return VConnUnreachable
	}
	return VOther
}

// TLS classifies a handshake failure. A reset during the handshake is the
// interesting case: the ClientHello is the first packet carrying the server
// name in the clear, so a reset here is consistent with name-based inspection.
func TLS(err error) string {
	var he *x509.HostnameError
	if errors.As(err, &he) {
		return VTLSCertName
	}
	var ae x509.UnknownAuthorityError
	if errors.As(err, &ae) {
		return VTLSCertInvalid
	}
	var ie x509.CertificateInvalidError
	if errors.As(err, &ie) {
		return VTLSCertInvalid
	}
	switch {
	case isTimeout(err):
		return VTLSTimeout
	case isReset(err):
		return VTLSReset
	}
	if strings.Contains(strings.ToLower(err.Error()), "certificate") {
		return VTLSCertInvalid
	}
	return VTLSFail
}

// HTTP classifies a failure once the connection is established. gotResponse
// distinguishes "died while sending" from "died while reading", which is what
// separates request-triggered blocking from volume-triggered blocking.
func HTTP(err error, gotResponse bool) (stage, verdict string) {
	stage = schema.StageRequest
	if gotResponse {
		stage = schema.StageResponse
	}
	if strings.Contains(err.Error(), "stopped after") {
		return stage, VTooManyRedirect
	}
	switch {
	case isTimeout(err) && gotResponse:
		return stage, VRespTimeout
	case isTimeout(err):
		return stage, VReqTimeout
	case isReset(err) && gotResponse:
		return stage, VRespReset
	case isReset(err):
		return stage, VReqReset
	}
	if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "EOF") {
		if gotResponse {
			return stage, VRespTruncated
		}
		return stage, VRespEmpty
	}
	return stage, VOther
}
