#!/usr/bin/env python3
"""Schema -> contract parity audit (docs/dev/api-ui-parity.md).

The companion to scripts/api-ui-parity.py, and it exists because that script has
a blind spot it cannot close from where it stands.

`api-ui-parity.py` compares the OpenAPI contract against the screens. So it
answers "is every field the API declares reachable from a screen" and reported
NO GAPS on a branch where `github_installation_id` - the field that makes the
GitHub App usable at all - was present in the migration, the sqlc queries, the
store, the domain struct, the scheduler and the agent, and absent from
openapi.yaml and all three handler DTOs. Correctly and uselessly: there was no
request field to check, because the contract never had one.

This script asks the question one layer down: **is every operator-facing column
the database stores reachable through the contract at all.** A column the panel
persists, the scheduler reads and the agent honours, with no field in the API, is
a feature that was built and cannot be used.

    python3 scripts/schema-contract-parity.py            # report
    python3 scripts/schema-contract-parity.py --check    # non-zero exit on a gap

It reads core/store/db/models.go - sqlc's own output, so the column list cannot
drift from the schema - and every property name in core/api/rest/openapi.yaml.
"""
import re
import sys
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
MODELS = ROOT / "core/store/db/models.go"
SPEC = ROOT / "core/api/rest/openapi.yaml"


def die(message):
    sys.exit("schema-contract-parity: " + message)


# Tables whose rows are machinery rather than configuration. An operator never
# chooses anything in them, so a column with no contract field is correct.
INTERNAL_TABLES = {
    "AgentChannel", "AgentUpdateStatus", "AuditEvent", "Deployment", "Revision",
    "ComposeRevision", "DatabaseRevision", "DatabaseBackup", "DatabaseRestore",
    "InboxItem", "WebhookDelivery", "Session", "JoinToken", "AgentCert",
    "SchemaMigration", "QuotaState", "DnsRecord", "MailboxLink", "PreviewLease",
    "ServerMetric", "AppMetric", "LogLine", "GithubInstallation", "AccessRequest",
    "TeamInvite", "EmailChange", "TotpEnrollment", "PanelRelease", "RevisionEnvVar",
    # Metric aggregates: written by the collector, read by charts, never chosen.
    "RequestMetric", "RequestPath", "ResourceMetric", "ResourceDiskUsage",
    "ResourceUsageDaily", "UserAvatar",
    # Grants and approvals: created by an act (break glass, approve), never
    # edited as configuration.
    "BreakGlassGrant", "DeployApproval", "AlertEvent", "TotpRecoveryCode",
    # Snapshots and CA material: machinery of disaster recovery and identity.
    "PanelSnapshot", "PlaneSnapshot", "PlaneCa", "PanelUpgrade", "PanelTl",
    "VolumeBackupRecord", "WebhookDeliveryAttempt",
    # Observed rows: a preview's lease, a task's run, a status probe's result.
    # Written by the system, read on a screen, never chosen.
    "Preview", "ScheduledTaskRun", "StatusEvaluation", "StatusInterval",
    "BackupRecord",
}

# Column-name shapes that are never operator-facing wherever they appear.
# NOT `.*_id`. That was the first version's filter and it would have excluded
# `github_installation_id` - the very column this script was written after. The
# foreign keys an operator CHOOSES (a deploy key, a registry, an App
# installation, a target server) are precisely the ones that go missing, so only
# the structural links are excluded: the row's own identity, and the parent the
# URL already carries.
STRUCTURAL_ID = re.compile(
    r"^(id|environment_id|project_id|team_id|application_id|database_id|server_id|"
    r"status_page_id|"
    r"user_id|stack_id|revision_id|deployment_id|page_id|domain_id|rule_id|"
    r"target_id|task_id|endpoint_id|installation_id|zone_id|notifier_id)$"
)

INTERNAL_COLUMN = re.compile(
    r"^(created|updated|.*_at|.*_ct|.*_nonce|.*_hash|.*_hint|"
    r"status|status_detail|.*_state|observed_.*|desired_revision|.*_token|"
    r".*_secret|.*_pem|.*_key|.*_fingerprint|.*_version|.*_count|.*_bytes|"
    r".*_percent|.*_seconds_observed|last_.*|.*_label|.*_by|pending_.*|"
    r".*_enc|.*_since|.*_until)$"
)

# Columns that are genuinely internal despite not matching the shapes above.
# Each is a decision with a reason, not a to-do: silencing one has to be an
# argument a reviewer can disagree with.
EXEMPT = {
    ("Application", "restart_token"): "bumped by the restart endpoint, never typed",
    ("Application", "env_applied"): "observed, not chosen",
    ("Application", "replica_status"): "observed from the agent",
    ("Application", "webhook"): "the id is generated; the secret has its own rotate route",
    ("Application", "maintenance"): "set by the maintenance endpoint, not a config field",
    ("Application", "ip_allowlist"): "set by the access-control endpoint",
    ("Application", "preview_password"): "write-only, its own endpoint",
    ("Database", "root_user"): "engine-determined, not operator-configurable at v1",
    ("Database", "volume_name"): "derived from the id",
    ("Database", "data_path"): "engine-determined",
    ("Database", "network"): "derived from the environment",
    ("Server", "role"): "chosen at join time, in the join command",
    ("Project", "slug"): "derived from the name",
    ("DnsZone", "provider_zone_id"): "the provider's own id for the zone, observed not chosen",
    # One provider is supported (managed-email.md), so the column records which
    # one that was rather than offering a choice. It becomes operator-facing the
    # day a second provider lands, and this line is what should be deleted then.
    ("MailProvider", "kind"): "one provider is supported; the column is a record, not a choice",
}


def go_field_to_column(name: str) -> str:
    """`RuntimeServerID` -> `runtime_server_id`, sqlc's own convention."""
    s = re.sub(r"(ID|URL|PEM|TTL|CPU|TLS|DNS|ACME|HTTP|S3)(?=[A-Z]|$)", lambda m: m.group(1).capitalize(), name)
    s = re.sub(r"(?<!^)(?=[A-Z])", "_", s)
    return s.lower()


def tables():
    """Every sqlc model, as {StructName: [column, ...]}."""
    if not MODELS.is_file():
        die(f"no sqlc models at {MODELS} - run this from a CypherPanel checkout")
    out, current = {}, None
    for line in MODELS.read_text().splitlines():
        m = re.match(r"^type (\w+) struct \{", line)
        if m:
            current = m.group(1)
            out[current] = []
            continue
        if current and line.strip() == "}":
            current = None
            continue
        if current:
            f = re.match(r"^\t(\w+)\s+\S", line)
            if f:
                out[current].append(go_field_to_column(f.group(1)))
    if not out:
        die(f"{MODELS} declared no structs - has sqlc's output format changed?")
    return out


# Which contract schemas describe which table. Explicit rather than inferred,
# because the question has to be asked PER TABLE.
#
# A flat "is this name anywhere in the contract" set was the first version and it
# could not catch its own motivating bug: every schema has an `id`, so any
# column ending in `_id` matched something and passed. `github_installation_id`
# sailed through a check written because `github_installation_id` was missing.
TABLE_SCHEMAS = {
    "Application": ["Application", "CreateApplicationRequest", "PatchApplicationRequest"],
    "Database": ["Database", "CreateDatabaseRequest", "PatchDatabaseRequest"],
    "Server": ["Server", "CreateServerRequest", "PatchServerRequest"],
    "Project": ["Project", "CreateProjectRequest", "UpdateProjectRequest"],
    "Environment": ["Environment", "CreateEnvironmentRequest", "UpdateEnvironmentRequest"],
    "ComposeStack": ["ComposeStack", "CreateComposeStackRequest", "UpdateComposeStackRequest"],
    "ScheduledTask": ["ScheduledTask", "CreateScheduledTaskRequest", "UpdateScheduledTaskRequest"],
    "Registry": ["Registry", "CreateRegistryRequest", "UpdateRegistryRequest"],
    "DeployKey": ["DeployKey", "CreateDeployKeyRequest"],
    "Notifier": ["Notifier", "CreateNotifierRequest", "UpdateNotifierRequest"],
    "BackupTarget": ["BackupTarget", "CreateBackupTargetRequest", "UpdateBackupTargetRequest"],
    "WebhookEndpoint": ["WebhookEndpoint", "CreateWebhookEndpointRequest", "UpdateWebhookEndpointRequest"],
    "StatusPage": ["StatusPage", "CreateStatusPageRequest", "UpdateStatusPageRequest"],
    "MailDomain": ["MailDomain", "CreateMailDomainRequest"],
    "AlertRule": ["AlertRule", "CreateAlertRuleRequest", "UpdateAlertRuleRequest"],
    "ResourceQuota": ["ResourceQuota", "SetQuotaRequest"],
    "Team": ["Team", "CreateTeamRequest"],
    "User": ["User", "CreateUserRequest", "TOTPStatus"],
    "SharedVariable": ["SharedVariable", "CreateSharedVariableRequest", "UpdateSharedVariableRequest"],
    "BackupSchedule": ["BackupSchedule", "SetBackupScheduleRequest"],
    "GithubApp": ["GitHubApp", "SetGitHubAppRequest"],
    "DnsProvider": ["DNSSettings", "SetPanelDNSRequest"],
    "ProtectionPolicy": ["EnvironmentProtection", "SetProtectionRequest"],
    "ApiToken": ["ApiToken", "CreateTokenRequest"],
    "AppEnvVar": ["EnvVarKeys", "SetEnvVarRequest"],
    "ComposeEnvVar": ["EnvVarKeys", "SetEnvVarRequest"],
    "LogDrain": ["LogDrain", "CreateLogDrainRequest", "UpdateLogDrainRequest"],
    "MailProvider": ["MailProviderStatus", "MailProviderConfig"],
    "PanelMail": ["PanelMailSettings", "SetPanelMailRequest"],
    "MetricsSetting": ["MetricsSettings", "SetMetricsSettingsRequest"],
    "InboxPreference": ["InboxPreferences", "SetInboxPreferencesRequest"],
    "EnvironmentProtection": ["EnvironmentProtection", "SetProtectionRequest"],
    "FreezeWindow": ["FreezeWindow", "FreezeWindowInput"],
    "DnsZone": ["DNSZone"],
    "TeamMember": ["TeamMember", "AddTeamMemberRequest", "ChangeMemberRoleRequest"],
    "Mailbox": ["Mailbox", "CreateMailboxRequest"],
    "StatusPageComponent": ["StatusPageComponent", "SetStatusPageComponentsRequest"],
    "PlaneDrConfig": ["PlaneDisasterRecovery"],
    "BackupRecord": ["DatabaseBackup"],
    "VolumeBackup": ["VolumeBackup", "BackupSchedule", "SetBackupScheduleRequest"],
}


def query_parameters(doc):
    """Every query-parameter name in the document.

    A capability does not have to be a body field. Deleting a database's volume
    is `?delete_volume=true` on the DELETE, which is a perfectly good way to
    express a destructive opt-in - and ignoring that channel made this script
    report it as unreachable when it is not.
    """
    names = set()

    def scan(params):
        for q in params or []:
            # Query AND path: `PUT /applications/{id}/env/{key}` carries the
            # env var's key in the URL, which is a perfectly good way to
            # address it — counting only bodies reported it as unreachable.
            if isinstance(q, dict) and q.get("in") in ("query", "path") and q.get("name"):
                names.add(q["name"])

    for item in (doc.get("paths") or {}).values():
        if not isinstance(item, dict):
            continue
        scan(item.get("parameters"))
        for op in item.values():
            if isinstance(op, dict):
                scan(op.get("parameters"))
    return names


def load_spec():
    if not SPEC.is_file():
        die(f"no OpenAPI spec at {SPEC} - run this from a CypherPanel checkout")
    try:
        import yaml
    except ImportError:
        die("PyYAML is not installed (pip install pyyaml)")
    try:
        doc = yaml.safe_load(SPEC.read_text())
    except yaml.YAMLError as exc:
        die(f"{SPEC} is not valid YAML: {exc}")
    if not isinstance(doc, dict):
        die(f"{SPEC} is not an OpenAPI document")
    return doc.get("components", {}).get("schemas", {}) or {}, query_parameters(doc)


def properties_of(schemas, name, depth=0, seen=None):
    """Every property name a schema can carry, following $ref and allOf."""
    seen = seen or set()
    if depth > 5 or name in seen or name not in schemas:
        return set()
    seen.add(name)
    out = set()

    def walk(node, d):
        if d > 5 or not isinstance(node, dict):
            return
        if "$ref" in node:
            out.update(properties_of(schemas, node["$ref"].split("/")[-1], d + 1, seen))
            return
        for part in node.get("allOf") or []:
            walk(part, d + 1)
        if isinstance(node.get("items"), dict):
            # An array's element schema carries properties too: a status page's
            # components are `components: [{ resource_kind, label, ... }]`, and
            # not following items reported every one of them as unreachable.
            walk(node["items"], d + 1)
        for prop, sub in (node.get("properties") or {}).items():
            out.add(prop)
            if isinstance(sub, dict):
                walk(sub, d + 1)

    walk(schemas[name], depth)
    return out


def main():
    schemas, query = load_spec()
    gaps, unmapped = [], []
    checked = 0
    for table, columns in sorted(tables().items()):
        if table in INTERNAL_TABLES:
            continue
        names = TABLE_SCHEMAS.get(table)
        if names is None:
            # Silence is the failure mode this whole file exists to prevent, so
            # an unmapped table is REPORTED rather than skipped.
            unmapped.append(table)
            continue
        known = set(query)
        for n in names:
            known |= properties_of(schemas, n)
        if not (known - query):
            unmapped.append(f"{table} (schemas {', '.join(names)} declare nothing)")
            continue
        for col in columns:
            if STRUCTURAL_ID.match(col) or INTERNAL_COLUMN.match(col):
                continue
            if any(EXEMPT.get((table, p)) for p in (col, col.rsplit("_", 1)[0])):
                continue
            checked += 1
            # The contract NESTS where the table flattens: `route_domain` is
            # `domain` inside a `route` object, `health_interval_seconds` is
            # `interval_seconds` inside `health`, `build_push_repository` is
            # `push_repository` inside `build`. So every suffix counts, not just
            # the last segment - matching only the leaf reported three fields
            # that are plainly in the contract.
            parts = col.split("_")
            suffixes = {"_".join(parts[i:]) for i in range(len(parts))}
            # A bare `id` is never evidence: every schema has one, so allowing
            # it lets ANY column ending in `_id` match. That is the third way
            # this check failed to catch `github_installation_id`, and the
            # foreign keys an operator chooses are exactly the ones at stake.
            suffixes.discard("id")
            if suffixes & known:
                continue
            gaps.append((table, col))

    print(f"checked {checked} operator-facing columns against the OpenAPI contract")
    if unmapped:
        print(f"\n{len(unmapped)} table(s) not mapped to a schema, so NOT checked:")
        for t in unmapped:
            print(f"    {t}")
        print("    (add them to TABLE_SCHEMAS, or to INTERNAL_TABLES with a reason)")
    if gaps:
        print(f"\n{len(gaps)} column(s) the contract cannot express:")
        for table, col in gaps:
            print(f"    {table}.{col}")
        print(
            "\nEach is something the database stores and no client can set or read.\n"
            "Either add it to core/api/rest/openapi.yaml and its handler DTOs, or\n"
            "record it in this script's EXEMPT map with the reason it is internal."
        )
    else:
        print("\nno gaps: every operator-facing column is expressible through the contract")

    if "--check" in sys.argv and gaps:
        sys.exit(1)


if __name__ == "__main__":
    main()
