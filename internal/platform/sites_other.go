//go:build !linux

package platform

import "context"

type stubSites struct{}

func newSites() Sites { return stubSites{} }

func (stubSites) Provision(context.Context, SiteSpec) error          { return ErrUnsupported }
func (stubSites) Deprovision(context.Context, string, string) error { return ErrUnsupported }
