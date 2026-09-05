// Package clock measures how far this host's clock is from network time.
//
// Every row carries a millisecond timestamp and the pairing of a Russian
// measurement with its foreign control is a join across probes, so the
// offset is part of the data quality, not an operational detail. Servers run
// chrony and should read near zero; a handset runs on Android's network time
// and may read seconds. Either way the number is recorded per run rather than
// assumed.
//
// One SNTP exchange, no library, no persistence, never fatal.
package clock

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

// Servers tried in order. Public pools; one request per run.
var Servers = []string{"time.cloudflare.com:123", "time.google.com:123", "pool.ntp.org:123"}

const ntpEpochOffset = 2208988800 // seconds between 1900 and 1970

// Offset returns local minus network time. A positive value means this clock
// runs ahead.
func Offset(ctx context.Context) (time.Duration, string, error) {
	var last error
	for _, s := range Servers {
		off, err := query(ctx, s)
		if err == nil {
			return off, s, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no servers")
	}
	return 0, "", last
}

func query(ctx context.Context, server string) (time.Duration, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := make([]byte, 48)
	req[0] = 0x23 // LI=0, VN=4, Mode=3 (client)
	t1 := time.Now()
	putTimestamp(req[40:], t1)
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}
	resp := make([]byte, 48)
	if _, err := conn.Read(resp); err != nil {
		return 0, err
	}
	t4 := time.Now()
	if resp[0]&0x07 != 4 { // mode 4 = server
		return 0, errors.New("not a server reply")
	}
	t2 := getTimestamp(resp[32:]) // receive
	t3 := getTimestamp(resp[40:]) // transmit
	// Standard SNTP: offset = ((t2 - t1) + (t3 - t4)) / 2.
	off := (t2.Sub(t1) + t3.Sub(t4)) / 2
	// Local minus network is the negation of "how far network is ahead".
	return -off, nil
}

func putTimestamp(b []byte, t time.Time) {
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) * (1 << 32) / 1e9
	binary.BigEndian.PutUint32(b[0:], uint32(secs))
	binary.BigEndian.PutUint32(b[4:], uint32(frac))
}

func getTimestamp(b []byte) time.Time {
	secs := int64(binary.BigEndian.Uint32(b[0:])) - ntpEpochOffset
	frac := int64(binary.BigEndian.Uint32(b[4:]))
	return time.Unix(secs, frac*1e9/(1<<32))
}
