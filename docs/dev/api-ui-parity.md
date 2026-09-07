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

## What it does not check

It does not check that a control *works*, that it is reachable by a viewer's
role, or that the field means what the screen says it means. It answers one
question — is this field mentioned on the screen that calls its endpoint — and
answers it every time, which is more than a reviewer's memory does.
