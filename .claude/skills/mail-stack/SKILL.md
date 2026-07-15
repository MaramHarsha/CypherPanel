---
name: mail-stack
description: Postfix/Dovecot mail provisioning — virtual mailbox/domain layout, quotas, Rspamd, and DKIM/SPF/DMARC. Use when working on email accounts, forwarders, spam, or mail authentication records.
---

# Mail Stack

> **Status: code-grounded (Phase 5).** Virtual-mailbox provisioning is implemented: `internal/mailstore` (MariaDB `virtual_domains`/`virtual_users` behind a `Manager` interface), `mail.create`/`mail.delete` agent tasks (auth-DB row + Maildir), bcrypt hashing in Core (BLF-CRYPT compatible — plaintext never in the payload), `mail_accounts` (migration 000013) + account-scoped REST with the `email_accounts` package limit, and auto-published MX/SPF/DMARC via [[dns-management]]. Reference MTA config: `docs/mail-setup.md`. **Still to build:** the DKIM signer + per-domain key, and Rspamd. Read [[agent-config-generators]] and [[dns-management]].

## Components (MVP default)

- **Postfix** (SMTP) + **Dovecot** (IMAP/POP3) — the singular MVP mail stack (not Exim; that's cPanel's). **Rspamd** for spam filtering.
- Virtual users, not system users, for mailboxes: mail accounts live in a **virtual mailbox database** (maps address → mailbox path, quota, password hash), so email accounts are decoupled from Linux logins.

## Config & layout

- Postfix/Dovecot configs are generated (see [[agent-config-generators]]): validate (`postfix check`, `doveconf`) then reload, paths via the distro path layer.
- Virtual domain/mailbox layout is consistent and per-account; mailbox storage lives under a predictable root (Maildir). Enforce the account's package `email_accounts` and mailbox **quota** limits.
- Authentication goes against the virtual mailbox DB; passwords hashed (Dovecot-compatible scheme), never plaintext.

## Deliverability records (DKIM/SPF/DMARC)

- Generate and publish **SPF, DKIM, and DMARC** records via the DNS layer ([[dns-management]]) when a mail domain is provisioned — outbound mail from a fresh VPS is worthless without them.
- DKIM: generate a keypair per signing domain, publish the public key as a TXT record, configure the signer (Rspamd/OpenDKIM) with the private key (`0600`, never logged/committed).

## Relays & quotas

- Support quota-based outbound relaying through third-party smart hosts (SendGrid/SES/etc.) post-MVP — VPS IPs are frequently blacklisted; the relay config is per-account and rate-limited.
- Surface quota-exceeded and auth failures as clear user-facing errors.
