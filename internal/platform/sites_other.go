//go:build !linux

package platform

import "context"

type stubSites struct{}

func newSites() Sites { return stubSites{} }

func (stubSites) Provision(context.Context, SiteSpec) error            { return ErrUnsupported }
func (stubSites) Deprovision(context.Context, string, string) error    { return ErrUnsupported }
func (stubSites) RemovePHPPool(context.Context, string, string) error  { return ErrUnsupported }
func (stubSites) InstallCertificate(context.Context, string, []byte, string, []byte) error {
	return ErrUnsupported
}
func (stubSites) ApplyVHost(context.Context, string, []byte) error { return ErrUnsupported }
