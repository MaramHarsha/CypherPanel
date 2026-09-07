package rest

// The rollback flag reaches the service.
//
// It did not: handleStartUpgrade passed a literal `false`, which made the
// helper's entire downgrade branch — the one that refuses any version this host
// has not run, and otherwise keeps every row written since — unreachable from
// every client. The only backward move an owner had was the snapshot restore,
// which discards everything since the upgrade.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStartUpgradeRequestCarriesRollback(t *testing.T) {
	var req startUpgradeRequest
	body := `{"version":"v1.0.0","snapshot_retention_days":7,"rollback":true}`
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !req.Rollback {
		t.Fatal("rollback:true did not decode — the helper's downgrade branch stays unreachable")
	}
	// And the default is false, so an ordinary upgrade cannot become a
	// downgrade by omission.
	var plain startUpgradeRequest
	if err := json.NewDecoder(strings.NewReader(`{"version":"v2.0.0"}`)).Decode(&plain); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if plain.Rollback {
		t.Fatal("rollback defaulted to true; an upgrade must never imply a downgrade")
	}
}
