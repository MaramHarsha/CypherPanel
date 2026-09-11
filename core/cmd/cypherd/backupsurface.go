package main

import (
	"context"

	"github.com/MaramHarsha/cypherpanel/core/planebackup"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// backupSurface adapts the store's snapshot surface to planebackup.Copier.
//
// The one method that needs adapting is BeginLoad. Go matches a method by
// IDENTICAL signature, so a concrete `(*store.BackupTx, error)` does not
// satisfy an interface asking for `(planebackup.LoadTx, error)` even though
// every caller could use one as the other. Widening it here is the alternative
// to store importing planebackup, which would invert the dependency the
// injected table sorter exists to avoid.
type backupSurface struct{ *store.BackupConn }

func (b backupSurface) BeginLoad(ctx context.Context) (planebackup.LoadTx, error) {
	return b.BackupConn.BeginLoad(ctx)
}
