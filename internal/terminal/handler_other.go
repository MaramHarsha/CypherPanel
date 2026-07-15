//go:build !linux

package terminal

import "github.com/nats-io/nats.go"

// Serve is a non-Linux stub: the web terminal targets managed Linux servers.
func Serve(_ *nats.Conn, _ string) (*nats.Subscription, error) {
	return nil, nil
}
