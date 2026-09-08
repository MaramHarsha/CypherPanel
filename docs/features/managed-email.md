# Email for verified domains, via a provider

Design canvas `17e`–`17h` (Mail domain setup · Mailboxes · Webmail inbox ·
Compose). The largest proposed feature by a wide margin, and the one where
scoping honestly matters more than designing cleverly.

## 1. The sentence that defines the whole feature

From the design screen itself:

> The mail itself lives at a provider — CypherPanel creates the DNS, manages
> mailboxes, and gives you the inbox. **Your servers never send a byte of mail.**

That is not a hedge, it is the architecture. CypherPanel is **not** becoming a
mail server, and the distinction is worth stating in full because "self-hosted
panel adds email" reads like Postfix to most people:

| We do | We do not |
|---|---|
| Write MX/SPF/DKIM/DMARC through the DNS provider already connected | Run an MTA, an IMAP server, or a spam filter |
| Create and delete mailboxes through the mail provider's API | Store, queue, relay or deliver a message |
| Show the inbox, over IMAP, in the panel | Hold mail on your servers |
| Seal one provider credential | Manage IP reputation, blocklists, or PTR records |

Running mail is a specialist operational discipline with a reputation system
attached, and a panel whose vision non-negotiable 1 is *"one binary + one
database to install"* has no business shipping one. Vision also says *"Light.
Installing CypherPanel on a server should feel like adding a tool, not adopting
a platform"* — an MTA is the definition of adopting a platform.

## 2. Why it belongs here at all

Because the panel already owns the two hard parts and neither is mail-specific:

- **It owns the domain.** `dns-automation.md` already verifies a domain against
  a connected provider and writes records for it. Mail records are four more
  records against machinery that exists — and getting SPF, DKIM and DMARC right
  by hand is the single most common way self-hosted mail ends up in a spam
  folder.
- **It owns the identity.** A mailbox linked to a panel user is the thing that
  makes an inbox tab reasonable rather than a second login. The screen is
  explicit: *"Linking a mailbox to a panel user puts the Mail tab in their top
  bar with no second sign-in."*

An operator who has already pointed a domain at this panel is one click from
working email. That click is the feature.

## 3. Phasing, because this is four features wearing one coat

The four screens are not one slice, and pretending otherwise is how this lands
half-built. They ship in this order, and each stands alone:

**Phase 1 — Domain.** Connect a provider, enable mail on a verified domain,
write the records, show their live state. Value on its own: correct SPF/DKIM/
DMARC for a domain whose mail lives anywhere. No mailboxes, no inbox.

**Phase 2 — Mailboxes.** Create, list and delete mailboxes; show quota; link one
to a panel user; hand over IMAP/SMTP settings for a real mail client. Value on
its own: the operator manages accounts without leaving the panel and uses
Thunderbird or Mail.app for the reading.

**Phase 3 — Webmail, read.** The inbox, message list, reading pane, folders,
search. This is the largest single piece in the entire proposed set and it
deserves its own review before a line is written.

**Phase 4 — Webmail, write.** Compose, reply, reply-all, forward with
attachments, drafts.

**Only Phase 1 and Phase 2 are specified below in implementable detail.**
Phases 3 and 4 get their shape and their open questions, not a design — writing
a detailed spec for a webmail client before the mailbox layer exists would be
guessing at an interface to a system we have not integrated with yet.

## 4. Phase 1 — the domain

### The provider is an interface, with one implementation

`MailProvider` is consumer-defined in the plane (ENGINEERING rule 6) with the
operations the panel actually needs: `EnsureDomain`, `RequiredRecords`,
`ListMailboxes`, `CreateMailbox`, `DeleteMailbox`, `SetPassword`. **Migadu ships
first** because it is what the design screen names, it is a real hosted provider
with a documented admin API, and it does not require a business relationship to
evaluate.

The interface exists so the second implementation is a package, not a rewrite —
not because a second one is planned. One implementation behind an interface is
honest; three speculative ones are not.

The credential is **sealed exactly like a notifier's or a registry's**:
AES-256-GCM under the master key, never returned by any route, shown back as a
hint (`migadu · connected · token sealed`). It is unsealed at the one point it
is used, which is the plane making a provider call — the agent never sees it and
has no reason to.

### The records go through the DNS provider that is already connected

`EnsureDomain` returns the records the provider requires; the plane writes them
through the existing DNS automation and then reports their observed state, which
is what the screen's `● LIVE` column is. This is deliberately the same
`verified → record written → observed` path an application domain follows, not a
second one.

**Enabling mail requires the domain to be verified already.** A domain the panel
cannot manage records for is a domain where this feature can do nothing but
print instructions, and the screen's premise is *"domains you've already
verified"*.

DKIM is the one asymmetry worth noting: the provider generates the keypair and
publishes the public half for us to write. The panel never holds a DKIM private
key, which is one fewer secret in the system and is only possible because the
mail lives at the provider.

## 5. Phase 2 — mailboxes

Mailbox rows are **not stored by the panel as the source of truth**. The
provider owns them; the panel lists them through the API and caches nothing that
would go stale — the same posture the DNS zone list takes. What the panel *does*
store is the link table, because that is a panel fact the provider knows nothing
about:

```
mailbox_links:
  id, domain_id, address, user_id → users(id) ON DELETE SET NULL, created_at
  UNIQUE (address)
```

`ON DELETE SET NULL` because deleting a panel account must not delete a mailbox
— the mail is the person's, and orphaning the link is the correct degradation.

**The password is shown exactly once**, in the create response, and never
recoverable — the invitation-accept-URL rule, applied to a credential that has
the same shape. The panel cannot read it back because it does not have it: the
provider stores the hash and the panel forwards it once.

## 6. Phases 3 and 4 — webmail, and what is unresolved

The shape is IMAP over the provider, from the plane, with the panel as the
client. Four questions have to be answered before this is designed, and none of
them are answered here:

1. **Where does the IMAP connection live?** In the plane means the plane holds a
   long-lived authenticated session per reading user and proxies message bodies
   — real memory against a <300 MB budget, and mail content passing through a
   process whose logs must never see it. In the browser is not possible: IMAP is
   not HTTP.
2. **What is cached, and where?** An inbox that refetches every render is
   unusable; an inbox that caches message bodies in Postgres has put the user's
   mail in the panel's database, which contradicts §1's promise that mail lives
   at the provider.
3. **What does the threat model say?** Rendering third-party HTML email inside
   the panel's origin is a genuine XSS surface, and remote images are a tracking
   surface. Both have known answers (sanitise, proxy or block) and both need
   writing down before implementation, not after.
4. **What does it cost the footprint?** This is the first feature that would
   make the plane hold per-user session state. That is a change in kind, and it
   needs measuring against the budget rather than assuming.

Until those are answered, Phase 2 gives an operator working IMAP settings for a
real mail client, which is the 80% at a fraction of the risk.

## 7. Deliberately out of scope

- **Running an MTA or IMAP server.** §1. Not a phasing decision — a permanent
  one.
- **Mail for a domain the panel does not manage DNS for.** The feature's value
  is the records being correct; without record control it is a mailbox list with
  extra steps.
- **Shared mailboxes, distribution lists, calendars, contacts.** Provider
  features with provider UIs. The panel does the two things it is uniquely
  positioned to do and links out for the rest.
- **Migrating existing mail into a new provider.** IMAP-to-IMAP migration is a
  category of tool, not a button.
- **Using this transport for the panel's own mail.** `panel-mail.md` owns SMTP
  for email-change confirmations and invitations, deliberately separately: the
  panel must be able to send *before* any domain is verified, and coupling its
  own transport to a customer-facing mail feature would mean a mail outage takes
  invitations with it.
