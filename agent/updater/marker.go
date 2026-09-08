package updater

// The boot marker and probation (agent-updates.md §4c).
//
// The case this design exists for is "the new binary runs but cannot dial
// home". The rollback path must therefore never need the control plane, because
// the failure it survives is exactly "cannot reach the control plane": rolling
// back is `rename .prev → current` and exit, with no network and no decision.
//
// The attempt limit is 1, not 3, and the unit file is why. With
// StartLimitIntervalSec=60, StartLimitBurst=5 and RestartSec=5, a crash-looping
// binary burns five starts in twenty seconds and systemd puts the unit in
// `failed` — permanently off the bus, the outcome ADR-010 §5 forbids. One
// failed attempt plus one rollback plus the old binary is three starts in
// fifteen seconds. "Try it a few times, it might settle" spends the budget the
// rollback needs.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// markerName is the file, in the agent's STATE dir: it has to survive the
// binary swap and be read by whichever binary starts next.
const markerName = "update-marker.json"

// maxAttempts is one. See the package comment above for the arithmetic.
const maxAttempts = 1

// errCorruptMarker is returned instead of a bare nil so the swallow above is a
// visible decision at the call site rather than an accident.
var errCorruptMarker = errors.New("updater: boot marker is unreadable; treating it as absent")

type marker struct {
	Target    string    `json:"target"`
	Previous  string    `json:"previous"`
	Attempts  int       `json:"attempts"`
	Probation time.Time `json:"probation"`
	// BinaryPath is where the swap happened, recorded so a rollback does not
	// have to re-derive it from a process that may have been re-execed from
	// somewhere else.
	BinaryPath string `json:"binary_path"`
}

func markerPath(stateDir string) string { return filepath.Join(stateDir, markerName) }

func readMarker(stateDir string) (*marker, error) {
	b, err := os.ReadFile(markerPath(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("updater: reading boot marker: %w", err)
	}
	var m marker
	if err := json.Unmarshal(b, &m); err != nil {
		// A corrupt marker is treated as ABSENT rather than fatal, and that is
		// a decision rather than a swallow: the worst case is one un-rolled-back
		// update, while refusing to start over a malformed JSON file would take
		// the host off the bus — the one outcome ADR-010 §5 forbids.
		return nil, errCorruptMarker
	}
	return &m, nil
}

func writeMarker(stateDir string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("updater: encoding boot marker: %w", err)
	}
	return writeFileSynced(markerPath(stateDir), b, 0o600)
}

func clearMarker(stateDir string) error {
	if err := os.Remove(markerPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("updater: clearing boot marker: %w", err)
	}
	return nil
}
