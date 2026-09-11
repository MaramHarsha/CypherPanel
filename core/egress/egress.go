// Package egress guards the panel's own outbound requests against reaching
// inside its network (threat-model §5.11).
//
// It exists as one package rather than one copy per feature because it is a
// security control: two implementations of "which addresses may the plane
// dial" is how one of them quietly stops matching the other. Both callers —
// testing an unsaved notifier configuration, and testing a container registry
// credential — are the same shape of risk, so they get the same answer.
//
// The check runs in the dialer's Control hook, which is the whole point. The
// address handed to Control is the RESOLVED IP the socket is about to connect
// to, so a name that answers publicly on the first lookup and privately on the
// second is refused on the second — the DNS-rebinding gap a validation-time
// check leaves open. It also covers every redirect and every connection the
// client makes, not just the one whose URL was inspected.
package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// ErrPrivateDestination is returned when a request would reach an address that
// is not publicly routable. Distinguishable so a caller can explain the
// refusal rather than reporting it as a connection failure — "we will not dial
// that" and "nothing answered" are different facts.
var ErrPrivateDestination = errors.New("egress: that address is inside the panel's own network")

// PubliclyRoutable reports whether ip is an address the panel will dial.
//
// Everything an operator's own infrastructure answers on is excluded:
// loopback, RFC1918 and the IPv6 unique-local range, link-local (which is where
// cloud instance-metadata services live), the unspecified address, and
// multicast.
func PubliclyRoutable(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) would otherwise slip past the checks
	// above, which read the 16-byte form.
	if v4 := ip.To4(); v4 != nil && !ip.Equal(v4) {
		return PubliclyRoutable(v4)
	}
	return true
}

// Control is a net.Dialer Control hook. It runs after resolution, with the
// concrete address the socket is about to connect to.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPrivateDestination, address)
	}
	if !PubliclyRoutable(net.ParseIP(host)) {
		return fmt.Errorf("%w: %s", ErrPrivateDestination, host)
	}
	return nil
}

// Dialer dials only publicly routable addresses.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: Control}
}

// HTTPClient is a client that reaches only publicly routable addresses.
// Redirects are refused, as they are everywhere else the panel makes outbound
// requests: a receiver must not be able to bounce us somewhere we would not
// have gone on our own.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           Dialer(timeout).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Dial opens a TCP connection, refusing addresses that are not publicly
// routable. Used for the SMTP leg of an unsaved-config test. Bounded by the
// dialer's own timeout, so it needs no context of its own.
func Dial(addr string, timeout time.Duration) (net.Conn, error) {
	return Dialer(timeout).Dial("tcp", addr)
}
