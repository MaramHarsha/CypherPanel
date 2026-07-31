# Webhooks

CypherPanel delivers domain events to your own HTTP endpoints, signed with
HMAC-SHA256. Manage them at **Webhooks** in the sidebar (root admin only — an
endpoint receives the whole fleet's feed, which crosses reseller boundaries).

## Subscribing

Register an endpoint with a name, an absolute `http(s)` URL, and an optional set
of event subjects. **Selecting none means every event** — the common case for a
billing or CRM integration consuming the whole feed.

Subjects are validated at registration against what this build actually
publishes, so a typo fails loudly instead of silently never firing. The current
set:

```
events.account.created        events.account.activated
events.account.suspended      events.account.unsuspended
events.account.terminating    events.account.terminated
events.account.failed         events.account.ssl_issued
events.package.created        events.package.deleted
events.server.registered      events.reseller.created
```

Fetch it programmatically from `GET /api/v1/admin/webhooks/event-subjects`.

## The signing key

Generated for you at registration — an operator-chosen HMAC key is usually a
weak one — and shown **exactly once**, in the create response. It is stored
encrypted and never returned again. Lose it and you must delete the endpoint and
create a new one.

## Verifying a delivery

Each request carries:

| Header | Meaning |
| --- | --- |
| `X-CypherPanel-Event` | the event subject |
| `X-CypherPanel-Delivery` | delivery id (stable across retries — use it to deduplicate) |
| `X-CypherPanel-Timestamp` | Unix seconds |
| `X-CypherPanel-Signature` | `sha256=<hex>` |

The signature is `HMAC-SHA256(secret, "<timestamp>." + body)`. The timestamp is
**inside** the signed material, so a captured request cannot be replayed later
with a fresh timestamp header. Reject deliveries whose timestamp is outside your
tolerance window (5 minutes is a reasonable default), and always compare
signatures in constant time.

```python
import hmac, hashlib, time

def verify(secret: bytes, body: bytes, ts: str, sig: str, tolerance=300) -> bool:
    if abs(time.time() - int(ts)) > tolerance:
        return False
    expected = "sha256=" + hmac.new(secret, f"{ts}.".encode() + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, sig)
```

## Body

The raw domain event:

```json
{
  "id": "9f8c…",
  "subject": "events.account.created",
  "aggregate": "account",
  "aggregate_id": "…",
  "occurred_at": "2026-08-01T02:31:44Z",
  "data": { "id": "…", "username": "alice", "status": "provisioning" }
}
```

Events are facts: a minimal immutable snapshot, **never secrets**. If you need
more than the snapshot, re-fetch through the REST API using `aggregate_id`.

## Delivery, retry, and dead-lettering

A durable JetStream consumer turns each event into one delivery row per
interested endpoint, then acks. Retry is owned by those rows, not by JetStream
redelivery — one event fans out to many endpoints, and NAKing the event message
would re-deliver to endpoints that already succeeded.

- Any `2xx` marks the delivery `delivered`.
- Anything else retries with exponential backoff (30s, doubling, capped at 2h).
- After **6 attempts** the delivery is `dead`. At that point the endpoint is
  treated as down rather than flaky; fix it, then use **Redeliver**.

Deliveries are claimed with `FOR UPDATE SKIP LOCKED`, so running several Core
instances never double-sends one event to one endpoint.

Delivery never blocks the operation that produced the event. An account is
created whether or not your endpoint is reachable.

## Endpoint expectations

- Respond `2xx` quickly and do your work asynchronously. The client times out at
  15 seconds.
- Deduplicate on `X-CypherPanel-Delivery` (or the event `id`). Retries mean
  at-least-once, not exactly-once.
- Keep error bodies short — only the first 2 KB is captured into the delivery log.
