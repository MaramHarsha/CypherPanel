package notify

// Why the unsaved-config test is guarded and the saved path is not
// (threat-model §5.11). The mechanism itself lives in core/egress; this file
// records the decision that sends this one path through it.
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
// time, not at validation time, to avoid a DNS-rebinding gap".

import (
	"net"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/egress"
)

// ErrPrivateDestination is re-exported so the REST layer keeps matching on one
// error whichever package refused the address.
var ErrPrivateDestination = egress.ErrPrivateDestination

func guardedHTTPClient() *http.Client { return egress.HTTPClient(deliveryTimeout) }

// dialGuarded opens the SMTP leg of an unsaved-config test.
func dialGuarded(addr string) (net.Conn, error) { return egress.Dial(addr, deliveryTimeout) }
