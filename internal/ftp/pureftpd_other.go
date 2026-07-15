//go:build !linux

package ftp

import "context"

// PureFTPd is unavailable off Linux (dev machines).
type PureFTPd struct{}

func NewPureFTPd() *PureFTPd { return &PureFTPd{} }

func (PureFTPd) Provision(context.Context, Spec) error   { return ErrUnsupported }
func (PureFTPd) Deprovision(context.Context, string) error { return ErrUnsupported }
