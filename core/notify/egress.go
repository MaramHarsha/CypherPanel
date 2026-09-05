package notify

// Egress guard for the one path that tests a configuration nobody has saved
// (threat-model §5.11).
//
// A saved notifier keeps the posture that section records: http(s) only, no
// redirects, and private destinations deliberately not blocked, because the
// operator is the trust root and already runs containers on these machines.
// Testing an *unsaved* config is different in three ways that together turn
// that accepted risk into something sharper:
//
//   - Nothing is persisted, so there is no notifier row afterwards saying the
//     probe happened.
//   - The result comes back synchronously in the response body — "connection
//     refused" versus a timeout distinguishes a closed port from a filtered
//     one — where a saved notifier's failure only reaches a log line.
//   - It can be repeated at will with a different address each time.
//
// That is a port scanner with no trace, so this path gets the control §5.11
// names for exactly this situation: a destination check "resolved at request
// time, not at validation time, to avoid a DNS-rebinding gap". Enforcing it in
// the dialer's Control hook is what makes that true — the address handed to
// Control is the resolved IP the connection is about to use, so a name that
// answers publicly on the first lookup and privately on the second is refused
// on the second.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
)

// ErrPrivateDestination is returned when a test would reach an address that is
// not publicly routable. Distinguishable so the REST layer can explain the
// refusal rather than reporting it as a connection failure.
var ErrPrivateDestination = errors.New("notify: that address is inside the panel's own network")

// publiclyRoutable reports whether ip is an address the panel will dial while
// testing an unsaved configuration.
//
// Everything an operator's own infrastructure answers on is excluded: loopback,
// RFC1918 and the IPv6 unique-local range, link-local (which is where cloud
// instance-metadata services live), the unspecified address, and multicast.
func publiclyRoutable(ip net.IP) bool {
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
		return publiclyRoutable(v4)
	}
	return true
}

// guardControl is a net.Dialer Control hook. It runs after resolution, with the
// concrete address the socket is about to connect to.
func guardControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPrivateDestination, address)
	}
	if !publiclyRoutable(net.ParseIP(host)) {
		return fmt.Errorf("%w: %s", ErrPrivateDestination, host)
	}
	return nil
}

// guardedDialer dials only publicly routable addresses.
func guardedDialer() *net.Dialer {
	return &net.Dialer{Timeout: deliveryTimeout, Control: guardControl}
}

// guardedHTTPClient is the client the unsaved-config test uses. Redirects are
// refused here as they are everywhere else the panel makes outbound requests —
// a receiver must not be able to bounce us somewhere we would not have gone.
func guardedHTTPClient() *http.Client {
	return &http.Client{
		Timeout: deliveryTimeout,
		Transport: &http.Transport{
			DialContext:           guardedDialer().DialContext,
			TLSHandshakeTimeout:   deliveryTimeout,
			ResponseHeaderTimeout: deliveryTimeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// dialGuarded opens a TCP connection, refusing addresses that are not publicly
// routable. Used for the SMTP leg of an unsaved-config test. Bounded by the
// dialer's own timeout, so it needs no context of its own.
func dialGuarded(addr string) (net.Conn, error) {
	return guardedDialer().Dial("tcp", addr)
}
