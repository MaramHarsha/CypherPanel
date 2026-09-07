# Changelog

The in-panel **What's new** pane reads this file, embedded in the binary rather
than fetched (panel-updates.md §9). Two reasons: a fetched changelog is a second
outbound call with its own rate limit, and it renders prose from a network
source inside an operator-facing surface. Embedding costs a file that has to be
maintained per release; it buys a changelog that works air-gapped and cannot be
written by whoever compromises a feed.

Format: `## <version> — <YYYY-MM-DD>`, then one-line bullets. Nothing else is
parsed, and anything the parser does not recognise is skipped rather than
rendered.

## v0.4.0 — 2026-09-07

- Panel updates: a guided upgrade with a pre-flight, a fallback snapshot whose retention you pick, a health gate, and automatic rollback when the gate fails.
- Threshold alerts: tell a notifier when a server or an application crosses a line and stays there, with a backtest that shows what the rule would have done over the last week before you save it.
- Metrics and usage: CPU, memory, disk and request analytics per resource, and per-project attribution for a month.
- Application replicas: run one application as several containers, load-balanced by the Proxy on its node.
- Public status pages: one switch publishes a page saying whether a project's services are working, at your own domain.
- Volume backups: archive an application's flagged volumes to an S3 target on a schedule.
- Access control: IP allowlists and preview passphrases, enforced by the Proxy.
- Project export: download a project as a portable archive that runs anywhere Docker runs.

## v0.3.0 — 2026-08-20

- Compose stacks: deploy a docker-compose file as a first-class resource, with revisions and rollback.
- Deploy protection: per-environment approval rules, freeze windows and an audited break-glass override.
- Scheduled tasks: cron jobs that run inside an application's own container.
- Outbound webhooks: signed JSON deliveries for machines, alongside the notifiers that reach people.

## v0.2.0 — 2026-07-30

- Managed databases with S3 backups and restore.
- Preview environments: a pull request gets its own environment, destroyed when it closes.
- DNS automation and certificate issuance through the managed Proxy.

## v0.1.0 — 2026-07-01

- First release: projects, environments, applications, the deploy pipeline, and the dial-home agent.
