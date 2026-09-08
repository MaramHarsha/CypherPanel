# API → UI parity

The panel's standing claim is that **every mutating capability the API exposes
is reachable from the web UI**. `scripts/api-ui-parity.py` is what makes that
claim checkable instead of aspirational.

```
make parity        # report
python3 scripts/api-ui-parity.py --check    # non-zero exit if anything is missing
```

## Why it exists

The claim was false in seven places at once on the application settings screen
alone, and every one was found the same way: a deploy failed on a live panel,
and the control that would have fixed it did not exist. A private repository
with no deploy-key picker. A Next.js app listening on 3000 with no Port field.
A private base image with no registry picker.

What made those invisible for so long is worth naming, because it is the trap
this script exists to spring: **reading the database or the API tells you what
the panel CAN do, not what it SHOWS.** Every one of those fields was in the
schema, in the OpenAPI contract, in the PATCH handler and in the generated
client. The gap was the last two inches.

## What it checks

For every `POST`/`PUT`/`PATCH` operation in `core/api/rest/openapi.yaml` it
resolves the request body's fields, finds the hand-written file that calls that
operation's generated hook — both shapes, `useCreateThing` and the bare
`createThing`, because screens legitimately use either — and reports fields
that file never mentions.

Three properties make the output worth reading:

**Per screen, not globally.** `port` appeared in the create dialog while being
absent from settings. A global grep over the whole `web/` tree reported nothing
while the gap was real and a user was hitting it.

**It follows local imports, one level.** A screen may legitimately delegate its
request body to a shared component — `quota-meter.tsx` builds `SetQuotaRequest`
for both the project and the team quota screens — and checking only the caller
would report every field of it as missing. One level is enough for that shape
and keeps the check from degenerating into "somewhere in the app".

**It is deliberately conservative.** A field named *anywhere* in the calling
file counts as reachable, so it under-reports and never cries wolf. Anything it
flags is a field whose own screen does not mention it once.

## The two exemption lists

Both live at the top of the script, and an entry in either is a **decision with
a reason**, not a to-do. The value of the check is that a gap is loud, so
silencing one has to be an argument someone can read and disagree with.

`EXEMPT` holds `(schema, field)` pairs. Most of it is the create dialogs asking
the minimum and letting Settings own the rest (ui-principles §6) — and every
one of those fields was checked to be reachable on the resource's own settings
screen before it was listed. A deferral is only legitimate when there is
somewhere to defer *to*; the seven-field gap above is exactly what happens when
there is not.

`UNREACHABLE_BY_DESIGN` holds whole operations. It has one entry:
`addTeamMember`, because adding a member directly would create an account
nobody chose a password for, and invitations replaced it
(`invitations-and-access-requests.md` §1). The API keeps the route for scripts
that manage users out of band.

## What it does not check, and the gap that proves it

It does not check that a control *works*, that it is reachable by a viewer's
role, or that the field means what the screen says it means. It answers one
question — is this field mentioned on the screen that calls its endpoint — and
answers it every time, which is more than a reviewer's memory does.

**The limitation that matters most is the one it cannot see at all: this script
audits the OpenAPI spec against the UI, so a capability missing from the spec is
invisible to it.** That is not hypothetical. On 2026-09-07 an external review
found `github_installation_id` — the field that makes the GitHub App usable —
present in the migration, the sqlc queries, the store, the domain, the scheduler
and the agent, and absent from `openapi.yaml`, from the three handler DTOs and
from every screen. This script reported **no gaps** throughout, correctly and
uselessly: there was no request field to check, because the contract never had
one.

So a green run means *"every field the API declares is mentioned on the screen
that sends it"* and nothing more. It does not mean the API declares everything
the database can store, and it never will — the check that would catch that is a
different one, comparing the schema against the contract, and it does not exist
yet.

## Failure modes it refuses to guess through

The same review found the script dying with an opaque
`JSONDecodeError: Expecting value: line 1 column 1` whenever anything went
wrong, because it shelled out to a subprocess and never looked at the exit code.
It parses the YAML in-process now, and every failure is a sentence:

| Situation | What happens |
|---|---|
| PyYAML not installed | says so, and names `pip install pyyaml` |
| No spec at that path | says so, with the path |
| Spec is malformed YAML | prints the parser's own complaint |
| Spec parses to something that is not a mapping | says what it parsed as |
| **No `web/src` in the tree** | **refuses to run** |

The last row is the one worth the care. It does not raise: `rglob` over a
missing directory yields nothing, so the script would print a confident, fully
formatted report claiming every mutating endpoint in the API is unreachable from
the UI, and exit non-zero under `--check`. **A wrong answer that looks right is
worse than a traceback**, particularly from a script whose entire job is saying
that about other people's code.

## Prerequisites

`python3` and **PyYAML**. The repository's other tooling needs neither, so
`make parity` is the one target that can fail on a machine where everything else
builds — which is why the missing-dependency message names the install command
rather than leaving a stack trace.

## The layer below: `schema-contract-parity.py`

`api-ui-parity.py` compares the CONTRACT against the screens, so it cannot see a
capability the contract never declared. That blind spot is not hypothetical: it
reported **no gaps** on a branch where `github_installation_id` was present in
the migration, the sqlc queries, the store, the domain struct, the scheduler and
the agent, and absent from `openapi.yaml`. Correctly and uselessly.

`scripts/schema-contract-parity.py` asks the question one layer down: **is every
operator-facing column the database stores expressible through the contract at
all.** It reads `core/store/db/models.go` — sqlc's own output, so the column list
cannot drift from the schema — and resolves, per table, the schemas that describe
it, following `$ref`, `allOf` and array `items`, plus query and path parameters.

Both run from `make parity`.

### Getting it to actually work took three falsification rounds

The check was written because of `github_installation_id`, and the first three
versions of it **did not catch `github_installation_id`**. Each was found by
deleting the field from the contract and re-running, which is the only test that
matters for a tool like this:

1. The filter excluded `.*_id` as structural. That excludes the foreign keys an
   operator *chooses* — a deploy key, a registry, an App installation — which are
   exactly the ones that go missing. Now only a specific list of structural
   parent ids is excluded.
2. The match was against a flat set of every property name anywhere in the
   document. Every schema has an `id`, so any column ending in `_id` matched
   something. Now the check is per table, against the schemas that describe it.
3. The suffix match still allowed a bare `id` to count as evidence. Now it does
   not.

A check that cannot catch the bug it was written for is worse than no check: it
reports a clean run and stops anyone looking.

### What it found

`Database.delete_volume`. The column existed, the handler read it as a query
parameter, and the OpenAPI document never declared it — so no generated client
could send it. Meanwhile the panel's own delete dialog told the operator it
"deletes its data volume", and its confirmation listed "the container and its
data volume — permanently". The request sent nothing, the volume survived on the
host, and the operator had been told twice that their data was gone. That is a
worse shape than a missing control: a screen that promises what the API does not
do.

### Unmapped tables are reported, never skipped

A table with no entry in `TABLE_SCHEMAS` and none in `INTERNAL_TABLES` is
printed as *not checked*. Silence about what was not examined is the failure mode
this whole file exists to prevent.
