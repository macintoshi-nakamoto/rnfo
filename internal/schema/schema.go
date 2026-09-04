// Package schema defines the on-disk record formats.
//
// Rule: field meanings never change silently. If the meaning of
// an existing field changes, or a required field is removed, bump Version and
// document the change in docs/SCHEMA.md. Adding a new optional field does not
// require a bump, but must still be documented.
package schema

// Version is written into every record as "schema".
const Version = "v1"

// AgentVersion identifies the binary that produced a record.
const AgentVersion = "rnfo-probe/0.1.0"

// Network types a probe can sit on. "hosting" is a datacentre uplink,
// "eyeball" is a residential subscriber line, "mobile" is a cellular carrier.
// The distinction matters: Russian filtering equipment (TSPU) is deployed
// primarily at operators serving subscribers, so hosting and eyeball probes
// are expected to disagree. That disagreement is a result, not noise.
const (
	NetHosting = "hosting"
	NetEyeball = "eyeball"
	NetMobile  = "mobile"
)

// Stage records how far a connection got before it failed. This is the field
// curl cannot give us and the single most diagnostic value in the row: a reset
// at StageTCP is an address-level block, a reset at StageTLS after the
// ClientHello points at name-based inspection, and a reset at StageResponse
// points at content or volume triggers.
const (
	StageOK       = "ok"       // full response received
	StageDNS      = "dns"      // name resolution failed
	StageTCP      = "tcp"      // TCP handshake failed
	StageTLS      = "tls"      // TLS handshake failed
	StageRequest  = "request"  // failed while sending the request
	StageResponse = "response" // failed while receiving the response
)

// Measurement is one target, measured once, by one probe. Every attempted
// target produces exactly one of these, including failures. A missing row
// means the probe was down, and downtime must be reconstructible from the
// Run records below.
type Measurement struct {
	Schema  string `json:"schema"`
	RunID   string `json:"run_id"` // identical on every probe in the same slot; this is what pairs a Russian measurement with its foreign control
	TS      string `json:"ts"`     // RFC3339, UTC, milliseconds
	Probe   string `json:"probe"`
	Net     string `json:"net"`
	ASN     string `json:"asn"`     // "AS203273 NetCrafters OU"
	Country string `json:"country"` // probe country, from the identity lookup
	Region  string `json:"region"`  // probe region; never a full address
	Agent   string `json:"agent"`
	Profile string `json:"profile"` // which target set this run covered

	List     string `json:"list"`               // citizenlab-global | citizenlab-ru | controls | own
	Category string `json:"category,omitempty"` // Citizen Lab category code, e.g. NEWS
	Target   string `json:"target"`             // hostname
	URL      string `json:"url"`
	Attempt  int    `json:"attempt"` // 1 = first try, 2 = confirmation retry after a failure

	// Resolution.
	DNSRC    int      `json:"dns_rc"` // 0 ok, 1 nxdomain, 2 timeout, 3 other
	DNSErr   string   `json:"dns_err,omitempty"`
	DNSIPs   []string `json:"dns_ips,omitempty"`
	RemoteIP string   `json:"remote_ip,omitempty"` // the address actually dialled
	// IPFamily is which protocol the measurement actually ran over. It is a
	// variable, not a detail: filtering equipment is repeatedly reported to
	// treat IPv6 less thoroughly than IPv4, and probes differ in what they have.
	// Comparing a v6 measurement against a v4 one would be comparing two
	// different experiments.
	IPFamily string `json:"ip_family,omitempty"` // v4 | v6

	// Outcome.
	Stage   string `json:"stage"`
	Verdict string `json:"verdict"`
	CurlRC  int    `json:"curl_rc"` // compatibility mapping onto curl exit codes, so this dataset can be compared with curl-based measurements
	Err     string `json:"err,omitempty"`
	Errno   string `json:"errno,omitempty"` // ECONNRESET, ETIMEDOUT, ECONNREFUSED, EHOSTUNREACH

	// HTTP.
	HTTP        int    `json:"http"` // 0 when no response was received
	HTTPVersion string `json:"http_version,omitempty"`
	Redirects   int    `json:"redirects"`
	FinalURL    string `json:"final_url,omitempty"`
	Server      string `json:"server,omitempty"`

	// Timings, seconds. t_connect and t_tls are deltas, not cumulative:
	// t_dns = resolution, t_connect = TCP handshake alone,
	// t_tls = TLS handshake alone, t_firstbyte = request sent to first byte.
	TDNS       float64 `json:"t_dns"`
	TConnect   float64 `json:"t_connect"`
	TTLS       float64 `json:"t_tls"`
	TFirstByte float64 `json:"t_firstbyte"`
	TTotal     float64 `json:"t_total"`

	// TLS detail. A certificate that does not match the requested name is the
	// signature of interception, so the chain is fingerprinted, not trusted.
	TLSVersion   string `json:"tls_version,omitempty"`
	TLSCipher    string `json:"tls_cipher,omitempty"`
	TLSALPN      string `json:"tls_alpn,omitempty"`
	CertSubject  string `json:"cert_subject,omitempty"`
	CertIssuer   string `json:"cert_issuer,omitempty"`
	CertSHA256   string `json:"cert_sha256,omitempty"`
	CertNotAfter string `json:"cert_not_after,omitempty"`
	CertNameOK   *bool  `json:"cert_name_ok,omitempty"`

	// Body. Content is never stored: only its length, a hash and the title,
	// which is what a block page is identified by.
	BodyLen       int64  `json:"body_len"`
	BodySHA256    string `json:"body_sha256,omitempty"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
	Title         string `json:"title,omitempty"`

	// Wire counters. BytesRead is how much arrived before the connection died,
	// which is the measurement behind the reported "connections cut after
	// roughly 14-25 KB" behaviour. It cannot be recovered from curl output.
	BytesRead int64 `json:"bytes_read"`
	BytesSent int64 `json:"bytes_sent"`
}

// Run is one execution of the agent. It exists so that downtime is data:
// if measurements for a slot are absent, the absence of the Run record proves
// the probe was down rather than the targets being unreachable.
type Run struct {
	Schema     string  `json:"schema"`
	Kind       string  `json:"kind"` // "run"
	RunID      string  `json:"run_id"`
	Probe      string  `json:"probe"`
	Net        string  `json:"net"`
	ASN        string  `json:"asn"`
	Country    string  `json:"country"`
	Region     string  `json:"region"`
	Agent      string  `json:"agent"`
	Profile    string  `json:"profile"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at"`
	DurationS  float64 `json:"duration_s"`

	Targets   int            `json:"targets"`
	Rows      int            `json:"rows"`
	OK        int            `json:"ok"`
	Failed    int            `json:"failed"`
	ByVerdict map[string]int `json:"by_verdict"`

	// Controls are targets that must be reachable from anywhere. If most of
	// them fail, the probe lost connectivity and the rest of the run says
	// nothing about filtering. Analysis must discard such runs, so the counts
	// are recorded rather than inferred later.
	ControlsTotal int  `json:"controls_total"`
	ControlsOK    int  `json:"controls_ok"`
	Healthy       bool `json:"healthy"`

	ListManifest map[string]string `json:"list_manifest"` // list file -> sha256, so a row can always be traced to the exact target set
}

// Event records something that changed about the probe itself rather than
// about a target: an ASN change on a dynamic residential line, a clock jump,
// an agent upgrade. These are measurements too.
type Event struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"` // "event"
	TS     string `json:"ts"`
	Probe  string `json:"probe"`
	Type   string `json:"type"` // asn_changed | agent_started | clock_offset | identity_unknown
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Note   string `json:"note,omitempty"`
}
