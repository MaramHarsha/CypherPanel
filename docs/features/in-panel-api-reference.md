# In-panel API reference

Design canvas `14i`. The one proposed feature that needs no backend work at
all: the contract it documents already ships inside the binary.

## 1. Why this exists

Vision non-negotiable 3 is *API-first — no feature ships UI-only. If the API
can't do it, it doesn't exist.* A panel that makes that claim and then sends
you to a separate documentation site to act on it is undercutting its own
premise. The API is the product; the reference belongs where the product is.

There is a narrower reason too. The spec at `GET /api/v1/openapi.yaml` is
served **by the running binary**, so it is the contract of *this* panel at
*this* version — not the latest published docs for a version the operator may
not be on. A hosted reference site cannot make that promise, and the difference
matters most exactly when it bites: after an upgrade, when the operator is
holding a script that used to work.

## 2. What it is

A settings tab that renders the embedded spec, grouped by area, with a runnable
`curl` for each operation against **this panel's own base URL**.

Everything on the page is derived. There is no second copy of the API
description to drift: the summaries, the parameters, the request bodies and the
status codes are read from the same YAML that CI already fails on when it
diverges from the handlers (ENGINEERING rule 19). If a route gains a parameter,
this page gains it on the next build with nobody editing anything.

## 3. What it does not do

**It is not a request console.** The reference shows a `curl` you can copy; it
does not fire the request from the browser. That is a deliberate refusal and
worth recording, because "try it" is the obvious next feature and it is the
wrong one here:

- A console that runs as your session is a confused-deputy surface — every
  destructive route in the panel becomes one click away inside a documentation
  page, with none of the confirms the real screens carry. `ConfirmDestructive`
  exists precisely because `DELETE /projects/{id}` deserves a typed name.
- A console that runs as a *token* needs a live token in browser memory, which
  is the thing §5 spends its length avoiding.
- The panel already has a real UI for every operation. A console would be a
  second, worse one with no guard rails.

`curl` on the operator's own terminal keeps the blast radius where it belongs:
they read the command, they choose to run it.

**It does not document error bodies per route.** Every JSON error in this API
shares one shape and carries `trace_id`, so it is documented once as its own
section rather than repeated 198 times.

## 4. Grouping and the shape of the page

The canvas draws a left rail — *Deployments · Applications · Databases ·
Servers · Webhooks · Errors & rate limits* — and one operation expanded on the
right. The rail is the spec's own `tags`, in the order the spec declares them,
with two departures:

- **`Errors & rate limits` is a hand-written section**, not a tag. It explains
  the shared error envelope, `trace_id`, the `X-Request-Id` header, and the two
  throttles that actually exist (login, and the public invitation routes). None
  of that is per-operation.
- **Tags are relabelled for humans.** `compose-stacks` reads *Compose stacks*,
  `access-requests` reads *Access requests*. The mapping lives in one table in
  the route file; an unmapped tag falls back to its own name title-cased, so a
  new tag appears correctly without anyone remembering to add it.

Within a group, operations keep spec order — which is path order, so the list
and the collection endpoints sit above the item ones, as an operator scanning
for "how do I list X" expects.

## 5. "Copy with my token" — the security decision

This is the only part of the feature with a real decision in it, and the design
screen's own caption is the tension: *"'Copy with my token' inserts a real token
— the fastest path from reading to a working call."*

**The panel cannot do what that sentence literally describes.** `listTokens` is
metadata-only by construction (`core/api/rest/openapi.yaml`: *"List the caller's
personal access tokens (metadata only)"`), and `CreateTokenResponse` is the only
place a raw secret ever appears — shown once, never recoverable. There is no
"my token" to insert. Any design that pretends otherwise would require the
panel to store a readable copy of a credential, which is the opposite of every
other secret in this system (ui-principles §6, ENGINEERING rule 20).

So the feature ships three copy affordances, in ascending order of consequence:

1. **Copy** — the default. The command carries `$CYPHER_TOKEN`, a shell
   variable the operator sets themselves. Nothing sensitive touches the
   clipboard. This is what the button does when you have not asked for
   anything else.

2. **Copy with a new token** — the honest reading of the canvas's intent. It
   opens the ordinary create-token dialog, pre-filled with a name naming this
   page, and on success substitutes the raw secret into the copied command
   **once**, in memory, never stored. The dialog says what it is about to do
   before it does it, because creating a credential as a side effect of a copy
   would otherwise be a surprise.

3. Nothing else. There is no "remember my token", no `localStorage`, no
   sessionStorage. A credential on a clipboard is already a real exposure; one
   in browser storage is a worse one that outlives the tab.

The second affordance carries a line the first does not: *this command contains
a live credential — anything that can read your clipboard can read it.* That
sentence is the point. An operator who reads it and proceeds has made a
decision; one who never saw it has had it made for them.

**Scope follows the reader.** The created token gets exactly the abilities the
operation being copied needs, and where the operation is project-scoped it is
confined to that project (`ApiToken.project_id`). A token minted to run one
`GET` should not be able to delete a server, and the create call already
supports both, so nothing new is needed to honour it.

## 6. Base URL

The command uses the browser's own origin, which is the panel the reader is
looking at. `CYPHERD_PUBLIC_URL` is not consulted: the reference's job is to
produce a command that works from where the operator is standing, and if they
reached the panel at all then the origin they reached it at is reachable. Where
the two differ — a panel behind a proxy with a different public name — the
origin is still the correct answer for the person reading, and the wrong one
only for someone copying the command to a third machine, who has to adjust the
host anyway.

## 7. Deliberately out of scope

- **A request console.** §3, with reasons.
- **Client SDK generation.** The spec is served at a stable URL; anyone who
  wants a client can point their own generator at it, which is exactly what
  `web/` does (ENGINEERING rule 25). Shipping generators for languages nobody
  asked for is maintenance with no reader.
- **Per-route schema explorers.** Request and response bodies are shown as the
  spec's example or its property list, not as an expandable type tree. The type
  tree is the thing that turns a reference into a framework; the readable
  version fits on the screen the canvas drew.
- **Search across operations.** The panel already has ⌘K, and adding a second
  search box inside a settings tab teaches two habits. If reaching an operation
  by name proves to be the common path, the right home is the palette.
- **Versioned or historical docs.** This page documents the binary it is served
  by, and that is its whole advantage. A version picker would give it the same
  drift problem as a hosted site.
