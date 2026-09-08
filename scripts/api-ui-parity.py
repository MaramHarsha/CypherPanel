#!/usr/bin/env python3
"""API → UI parity audit (docs/dev/api-ui-parity.md).

The panel's claim is that every mutating capability the API exposes is reachable
from the web UI. That claim was false in seven places at once on the application
settings screen alone, and each was found the same way: a deploy failed, and the
control that would have fixed it did not exist.

This finds them mechanically instead.

For every mutating operation it resolves the request body's fields, finds the
hand-written file that calls that operation's generated hook, and reports fields
that file never mentions. Per SCREEN, not globally — `port` appeared in the
create dialog while being absent from settings, so a global check reported
nothing while the gap was real.

It is deliberately conservative: a field named anywhere in the calling file
counts as reachable, so it under-reports and never cries wolf. Anything it flags
is a field whose own screen does not mention it once.

    python3 scripts/api-ui-parity.py            # report
    python3 scripts/api-ui-parity.py --check    # non-zero exit if anything is missing
"""
import json, sys, pathlib, re, collections

ROOT = pathlib.Path(__file__).resolve().parent.parent
WEB = ROOT / "web/src"

# Fields a screen legitimately does not render, with the reason. An exemption
# list a reviewer can audit beats a checker nobody runs because it is noisy.
EXEMPT = {
    # Handled by its own dedicated card rather than the settings form.
    ("PatchApplicationRequest", "replicas"): "the scaling card on the Overview tab",
    # Set once at creation; changing it would rewrite history rather than config.
    ("CreateApplicationRequest", "env_vars"): "the Env tab owns variables after creation",
    ("CreateComposeStackRequest", "env_vars"): "the stack's own env editor after creation",
}

# The create dialogs ask the MINIMUM and let Settings own the rest
# (ui-principles §6). Each field below was checked to be reachable on the
# resource's own settings screen before it was listed here — a deferral is only
# legitimate when there is somewhere to defer TO, and the seven-field gap on the
# application settings screen is exactly what happens when there is not.
_CREATE_DEFERRED = "asked on the resource's settings screen instead — the create dialog asks the minimum"
for _field in (
    "cpu_limit", "memory_limit_mb", "volumes",
    "registry_id", "push_registry_id", "push_repository",
    "health", "interval_seconds", "timeout_seconds",
    "preview_enabled", "preview_base_domain", "preview_ttl_hours",
):
    EXEMPT[("CreateApplicationRequest", _field)] = _CREATE_DEFERRED
for _field in ("cpu_limit", "memory_limit_mb"):
    EXEMPT[("CreateDatabaseRequest", _field)] = _CREATE_DEFERRED

# Whole operations the panel deliberately does not call. An entry here is a
# DECISION with a reason, not a to-do: the checker's whole value is that an
# unreachable endpoint is loud, so silencing one has to be an argument.
UNREACHABLE_BY_DESIGN = {
    # invitations-and-access-requests.md §1: adding a member directly would
    # create an account nobody chose a password for, so the panel invites an
    # address instead and the invitee signs themselves in. The API keeps the
    # route for scripts that manage users out of band.
    "addTeamMember": "invitations replaced direct member-adding (invite-member-dialog.tsx)",
}

def die(message):
    """Exit with a sentence, never a traceback.

    Every failure below used to surface as an opaque
    `JSONDecodeError: Expecting value: line 1 column 1` from a subprocess whose
    exit code nobody looked at — which is the same class of bug this script
    exists to find.
    """
    sys.exit("api-ui-parity: " + message)

def load_spec():
    """The OpenAPI document, or a refusal that says what went wrong."""
    spec_path = ROOT / "core/api/rest/openapi.yaml"
    if not spec_path.is_file():
        die(f"no OpenAPI spec at {spec_path} — run this from a CypherPanel checkout")
    try:
        import yaml
    except ImportError:
        die("PyYAML is not installed (pip install pyyaml). This script parses the "
            "OpenAPI spec, and there is no useful answer without it.")
    try:
        spec = yaml.safe_load(spec_path.read_text())
    except yaml.YAMLError as exc:
        die(f"{spec_path} is not valid YAML: {exc}")
    # An empty or scalar document parses cleanly and is not a spec. Without this
    # the failure is an AttributeError on NoneType four frames later.
    if not isinstance(spec, dict):
        die(f"{spec_path} is not an OpenAPI document (parsed as {type(spec).__name__})")
    return spec

def check_tree():
    """Refuse to audit a tree that cannot be audited.

    This is the failure mode worth the most care, because it does not raise: a
    missing `web/src` makes `rglob` yield nothing, and the script then prints a
    confident, fully formatted report saying every mutating endpoint in the API
    is unreachable from the UI. A wrong answer that looks right is worse than a
    traceback, and this script's whole job is saying so about other people's
    code.
    """
    if not WEB.is_dir():
        die(f"no web sources at {WEB} — this audit compares the API against the "
            "panel's screens, and there are none here to compare against")

def fields_of(schemas, node, depth=0):
    if depth > 4 or not isinstance(node, dict):
        return
    if "$ref" in node:
        yield from fields_of(schemas, schemas.get(node["$ref"].split("/")[-1], {}), depth + 1)
        return
    for part in node.get("allOf", []) or []:
        yield from fields_of(schemas, part, depth + 1)
    for name, prop in (node.get("properties") or {}).items():
        yield name
        if isinstance(prop, dict) and ("properties" in prop or "$ref" in prop):
            yield from fields_of(schemas, prop, depth + 1)

def hand_written():
    """Every non-generated source file, by path."""
    return [p for p in WEB.rglob("*.ts*") if "/api/gen/" not in str(p)]

LOCAL_IMPORT = re.compile(r'from\s+"@/([^"]+)"')

def resolve(spec):
    """`@/components/quota-meter` -> the file on disk, or None."""
    base = WEB / spec
    for cand in (base, base.with_suffix(".tsx"), base.with_suffix(".ts"),
                 base / "index.tsx", base / "index.ts"):
        if cand.is_file():
            return cand
    return None

def reachable_text(path, files):
    """A caller's own source, plus the hand-written modules it imports.

    A screen may legitimately delegate its request body to a shared component —
    `quota-meter.tsx` builds `SetQuotaRequest` for both the project and the team
    quota screens — and checking only the caller file would report every field
    of it as missing. One level deep is enough for that shape and keeps the
    check from degenerating into "somewhere in the app".
    """
    text = files[path]
    for spec in LOCAL_IMPORT.findall(text):
        target = resolve(spec)
        if target is not None and target in files:
            text += files[target]
    return text

def main():
    check_tree()
    spec = load_spec()
    schemas = spec.get("components", {}).get("schemas", {})
    files = {p: p.read_text(errors="ignore") for p in hand_written()}
    if not files:
        die(f"{WEB} contains no TypeScript sources — every endpoint would report "
            "as unreachable, which would be a false report rather than a finding")

    findings, checked, unreachable = [], 0, []
    for path, ops in (spec.get("paths") or {}).items():
        for method, op in (ops or {}).items():
            if method not in ("post", "put", "patch") or not isinstance(op, dict):
                continue
            body = ((op.get("requestBody") or {}).get("content") or {}).get("application/json") or {}
            schema = body.get("schema")
            opid = op.get("operationId")
            if not schema or not opid:
                continue
            label = schema.get("$ref", "").split("/")[-1] or opid
            names = sorted(set(fields_of(schemas, schema)))
            if not names:
                continue

            # Who calls this operation. BOTH shapes count: orval generates a
            # `useX` hook and a bare `x()` function, and screens legitimately
            # use either — matching only the hook reported three screens as
            # having no caller when they simply called the function.
            hook = "use" + opid[0].upper() + opid[1:]
            callers = [p for p, src in files.items()
                       if re.search(rf"\b(?:{hook}|{re.escape(opid)})\b", src)]
            visible = {c: reachable_text(c, files) for c in callers}
            if not callers:
                if opid not in UNREACHABLE_BY_DESIGN:
                    unreachable.append((f"{method.upper()} {path}", opid))
                continue

            for name in names:
                if EXEMPT.get((label, name)):
                    continue
                checked += 1
                if not any(name in visible[c] for c in callers):
                    findings.append((label, name, opid,
                                     ", ".join(str(c.relative_to(ROOT)) for c in callers)))

    print(f"checked {checked} request fields against the screens that call their endpoint")
    if unreachable:
        print(f"\n{len(unreachable)} mutating endpoint(s) NO screen calls:")
        for route, opid in sorted(unreachable):
            print(f"    {route}   ({opid})")
    if findings:
        print(f"\n{len(findings)} field(s) whose own screen never mentions them:")
        by_screen = collections.defaultdict(list)
        for label, name, opid, where in findings:
            by_screen[where].append(f"{label}.{name}")
        for where, items in sorted(by_screen.items()):
            print(f"    {where}")
            for i in sorted(items):
                print(f"        {i}")
    if not findings and not unreachable:
        print("\nno gaps: every mutating endpoint has a caller, and every field is mentioned there")
    if "--check" in sys.argv and (findings or unreachable):
        return 1
    return 0

if __name__ == "__main__":
    sys.exit(main())
