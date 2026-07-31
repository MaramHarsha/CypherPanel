package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// Response shapes are the minimal subset each command prints. They mirror the
// OpenAPI schemas; unknown fields are ignored, so a Core that adds a field
// does not break an older CLI.

type serverRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IPAddress   string `json:"ip_address"`
	AgentStatus string `json:"agent_status"`
	Region      string `json:"region"`
}

type accountRow struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	PrimaryDomain string `json:"primary_domain"`
	Status        string `json:"status"`
	ServerName    string `json:"server_name"`
	PHPVersion    string `json:"php_version"`
	SSLStatus     string `json:"ssl_status"`
}

type dnsRow struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	TTL      int      `json:"ttl"`
	Contents []string `json:"contents"`
}

type destinationRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Schedule string `json:"schedule"`
}

type backupRow struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshot_id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	SizeBytes  int64     `json:"size_bytes"`
	StartedAt  time.Time `json:"started_at"`
	Error      string    `json:"error"`
}

// table writes aligned columns to stdout. Output is deliberately plain and
// tab-aligned so it stays greppable and pipeable.
func table(header []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// --- servers ---------------------------------------------------------------

func cmdServersList(ctx context.Context, args []string) error {
	fs := newFlagSet("servers list")
	region := fs.String("region", "", "filter to one region")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	var out []serverRow
	if err := c.get(ctx, "/admin/servers"+q("region", *region), &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out))
	for _, s := range out {
		rows = append(rows, []string{s.ID, s.Name, s.IPAddress, s.AgentStatus, dash(s.Region)})
	}
	table([]string{"ID", "NAME", "IP", "STATUS", "REGION"}, rows)
	return nil
}

// --- accounts --------------------------------------------------------------

func cmdAccountsList(ctx context.Context, args []string) error {
	fs := newFlagSet("accounts list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	var out []accountRow
	if err := c.get(ctx, "/admin/accounts", &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out))
	for _, a := range out {
		rows = append(rows, []string{a.ID, a.Username, a.PrimaryDomain, a.Status, a.ServerName, a.PHPVersion, dash(a.SSLStatus)})
	}
	table([]string{"ID", "USERNAME", "DOMAIN", "STATUS", "SERVER", "PHP", "SSL"}, rows)
	return nil
}

func cmdAccountsCreate(ctx context.Context, args []string) error {
	fs := newFlagSet("accounts create")
	username := fs.String("username", "", "account username (required)")
	email := fs.String("email", "", "contact email (required)")
	password := fs.String("password", "", "initial password (required)")
	domain := fs.String("domain", "", "primary domain (required)")
	serverID := fs.String("server", "", "server ID (required)")
	packageID := fs.String("package", "", "package ID (required)")
	phpVersion := fs.String("php", "", "PHP version (default: server default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *email == "" || *password == "" || *domain == "" || *serverID == "" || *packageID == "" {
		fs.Usage()
		return errors.New("username, email, password, domain, server and package are required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	body := map[string]string{
		"username": *username, "email": *email, "password": *password,
		"primary_domain": *domain, "server_id": *serverID, "package_id": *packageID,
	}
	if *phpVersion != "" {
		body["php_version"] = *phpVersion
	}
	var out accountRow
	if err := c.post(ctx, "/admin/accounts", body, &out); err != nil {
		return err
	}
	fmt.Printf("account %s created (%s) — provisioning asynchronously\n", out.Username, out.ID)
	return nil
}

func cmdAccountsAction(action string) commandFunc {
	return func(ctx context.Context, args []string) error {
		fs := newFlagSet("accounts " + action)
		id := fs.String("id", "", "account ID (required)")
		// Terminating destroys the account's Linux user and data, so it must be
		// an explicit second decision — never a bare --id away.
		yes := fs.Bool("yes", false, "confirm a destructive action")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" {
			fs.Usage()
			return errors.New("--id is required")
		}
		if action == "terminate" && !*yes {
			return errors.New("terminate permanently deletes the account and its data; re-run with --yes to confirm")
		}
		c, err := newClient(ctx)
		if err != nil {
			return err
		}
		if err := c.post(ctx, "/admin/accounts/"+*id+"/"+action, nil, nil); err != nil {
			return err
		}
		fmt.Printf("account %s: %s requested\n", *id, action)
		return nil
	}
}

// --- ssl -------------------------------------------------------------------

func cmdSSLIssue(ctx context.Context, args []string) error {
	fs := newFlagSet("ssl issue")
	id := fs.String("account", "", "account ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		fs.Usage()
		return errors.New("--account is required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	if err := c.post(ctx, "/admin/accounts/"+*id+"/ssl", nil, nil); err != nil {
		return err
	}
	fmt.Printf("certificate issuance requested for account %s\n", *id)
	return nil
}

// --- dns -------------------------------------------------------------------

func cmdDNSList(ctx context.Context, args []string) error {
	fs := newFlagSet("dns list")
	id := fs.String("account", "", "account ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		fs.Usage()
		return errors.New("--account is required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	var out []dnsRow
	if err := c.get(ctx, "/admin/accounts/"+*id+"/dns", &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out))
	for _, r := range out {
		rows = append(rows, []string{r.Name, r.Type, fmt.Sprint(r.TTL), strings.Join(r.Contents, " | ")})
	}
	table([]string{"NAME", "TYPE", "TTL", "VALUE"}, rows)
	return nil
}

func cmdDNSSet(ctx context.Context, args []string) error {
	fs := newFlagSet("dns set")
	id := fs.String("account", "", "account ID (required)")
	name := fs.String("name", "", "record name (required)")
	rtype := fs.String("type", "", "record type, e.g. A/MX/TXT (required)")
	value := fs.String("value", "", "record value (required; repeat with commas for multi-value)")
	ttl := fs.Int("ttl", 3600, "record TTL in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *name == "" || *rtype == "" || *value == "" {
		fs.Usage()
		return errors.New("--account, --name, --type and --value are required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"name": *name, "type": strings.ToUpper(*rtype), "ttl": *ttl,
		"contents": strings.Split(*value, ","),
	}
	if err := c.post(ctx, "/admin/accounts/"+*id+"/dns", body, nil); err != nil {
		return err
	}
	fmt.Printf("record %s %s upserted\n", *name, strings.ToUpper(*rtype))
	return nil
}

func cmdDNSDelete(ctx context.Context, args []string) error {
	fs := newFlagSet("dns delete")
	id := fs.String("account", "", "account ID (required)")
	name := fs.String("name", "", "record name (required)")
	rtype := fs.String("type", "", "record type (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *name == "" || *rtype == "" {
		fs.Usage()
		return errors.New("--account, --name and --type are required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	path := "/admin/accounts/" + *id + "/dns" + q("name", *name, "type", strings.ToUpper(*rtype))
	if err := c.delete(ctx, path); err != nil {
		return err
	}
	fmt.Printf("record %s %s deleted\n", *name, strings.ToUpper(*rtype))
	return nil
}

// --- backup ----------------------------------------------------------------

func cmdBackupDestinations(ctx context.Context, args []string) error {
	fs := newFlagSet("backup destinations")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	var out []destinationRow
	if err := c.get(ctx, "/admin/backup/destinations", &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out))
	for _, d := range out {
		rows = append(rows, []string{d.ID, d.Name, d.Kind, d.Schedule})
	}
	table([]string{"ID", "NAME", "KIND", "SCHEDULE"}, rows)
	return nil
}

func cmdBackupRun(ctx context.Context, args []string) error {
	fs := newFlagSet("backup run")
	id := fs.String("account", "", "account ID (required)")
	dest := fs.String("destination", "", "destination ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *dest == "" {
		fs.Usage()
		return errors.New("--account and --destination are required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	var out backupRow
	if err := c.post(ctx, "/admin/accounts/"+*id+"/backups", map[string]string{"destination_id": *dest}, &out); err != nil {
		return err
	}
	fmt.Printf("backup %s started for account %s\n", out.ID, *id)
	return nil
}

func cmdBackupList(ctx context.Context, args []string) error {
	fs := newFlagSet("backup list")
	id := fs.String("account", "", "account ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		fs.Usage()
		return errors.New("--account is required")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	var out []backupRow
	if err := c.get(ctx, "/admin/accounts/"+*id+"/backups", &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out))
	for _, b := range out {
		rows = append(rows, []string{
			b.ID, dash(b.SnapshotID), b.Kind, b.Status,
			b.StartedAt.Local().Format(time.RFC3339), dash(b.Error),
		})
	}
	table([]string{"ID", "SNAPSHOT", "KIND", "STATUS", "STARTED", "ERROR"}, rows)
	return nil
}

func cmdBackupRestore(ctx context.Context, args []string) error {
	fs := newFlagSet("backup restore")
	id := fs.String("account", "", "account ID (required)")
	backupID := fs.String("backup", "", "backup ID to restore from (required)")
	snapshot := fs.String("snapshot", "", "snapshot ID (required)")
	inPlace := fs.Bool("in-place", false, "overwrite the account's live files instead of restoring to staging")
	yes := fs.Bool("yes", false, "confirm an in-place restore")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *backupID == "" || *snapshot == "" {
		fs.Usage()
		return errors.New("--account, --backup and --snapshot are required")
	}
	// An in-place restore overwrites live data irreversibly; require the same
	// explicit second decision the UI's confirmation dialog demands.
	if *inPlace && !*yes {
		return errors.New("--in-place overwrites the account's live files; re-run with --yes to confirm")
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	target := ""
	if *inPlace {
		target = "home"
	}
	body := map[string]string{"snapshot_id": *snapshot, "target": target}
	if err := c.post(ctx, "/admin/accounts/"+*id+"/backups/"+*backupID+"/restore", body, nil); err != nil {
		return err
	}
	where := "the staging directory"
	if *inPlace {
		where = "the account's home directory"
	}
	fmt.Printf("restore of snapshot %s started into %s\n", *snapshot, where)
	return nil
}
