package dkim

import (
	"fmt"
	"os"
	"path/filepath"
)

// rspamdSigningConf is a single, domain-agnostic signing config. Rspamd
// expands $domain/$selector per message, so one file covers every hosted
// domain — writing one config per domain would mean a reload on every mailbox
// creation and a config tree that drifts out of sync with the key tree.
const rspamdSigningConf = `# Managed by CypherPanel — edits will be overwritten.
# Keys are generated per hosted domain; see the dkim_key_dir below.
enabled = true;
path = "%s/$domain/$selector.private";
selector = "%s";
# Sign for any hosted domain we hold a key for, and don't refuse to sign just
# because the authenticated username doesn't match the From domain (hosted
# accounts legitimately send as several addresses).
allow_username_mismatch = true;
allow_hdrfrom_mismatch = true;
use_domain = "header";
sign_authenticated = true;
sign_local = true;
`

// WriteRspamdConfig writes the DKIM signing config into rspamd's local.d
// override directory. Idempotent: the content is fully determined by the key
// directory and selector, so re-running converges.
//
// The caller reloads rspamd; this only renders the file.
func WriteRspamdConfig(localDir, keyDir, selector string) error {
	if selector == "" {
		selector = DefaultSelector
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return fmt.Errorf("dkim: creating rspamd config dir: %w", err)
	}
	path := filepath.Join(localDir, "dkim_signing.conf")
	content := fmt.Sprintf(rspamdSigningConf, keyDir, selector)

	// Skip the write when nothing changed so a mailbox creation does not churn
	// the file (and its mtime) on every task.
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("dkim: writing rspamd config: %w", err)
	}
	return nil
}
