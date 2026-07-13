//go:build !linux

package platform

import "context"

// stubSystemUsers lets the agent compile and unit-test on dev machines;
// every operation reports ErrUnsupported.
type stubSystemUsers struct{}

func newSystemUsers() SystemUsers {
	return stubSystemUsers{}
}

func (stubSystemUsers) Create(context.Context, string, string) error { return ErrUnsupported }
func (stubSystemUsers) Remove(context.Context, string) error         { return ErrUnsupported }
func (stubSystemUsers) Exists(context.Context, string) (bool, error) { return false, ErrUnsupported }
