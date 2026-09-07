#!/usr/bin/env python3
"""API -> UI parity audit.

Walks every request-body schema in openapi.yaml and asks whether the web app
mentions each field at all. It is deliberately CRUDE: a field name appearing
anywhere in web/src is counted as reachable. That means it under-reports (a
field named in a comment counts) and never over-reports — so anything it flags
is a field the UI does not mention ONCE, which is the failure we keep hitting.
"""
import json, re, subprocess, sys, pathlib, collections

root = pathlib.Path("/root/CypherPanel-backend")
spec = json.loads(subprocess.run(
    ["python3","-c","import sys,yaml,json;json.dump(yaml.safe_load(open(sys.argv[1])),sys.stdout)",
     str(root/"core/api/rest/openapi.yaml")],capture_output=True,text=True).stdout)

schemas = spec.get("components",{}).get("schemas",{})

def resolve(node, depth=0):
    """Yield (name, node) for every property of a schema, following $ref/allOf."""
    if depth > 4 or not isinstance(node, dict): return
    if "$ref" in node:
        target = node["$ref"].split("/")[-1]
        yield from resolve(schemas.get(target,{}), depth+1); return
    for part in node.get("allOf",[]) or []:
        yield from resolve(part, depth+1)
    for name, prop in (node.get("properties") or {}).items():
        yield name, prop
        if isinstance(prop,dict) and (prop.get("type")=="object" or "properties" in prop or "$ref" in prop):
            yield from resolve(prop, depth+1)

# Every request body across every mutating operation.
wanted = collections.defaultdict(set)   # schema name -> field names
for path, ops in (spec.get("paths") or {}).items():
    for method, op in (ops or {}).items():
        if method not in ("post","put","patch"): continue
        if not isinstance(op, dict): continue
        body = ((op.get("requestBody") or {}).get("content") or {}).get("application/json") or {}
        sch = body.get("schema")
        if not sch: continue
        label = sch.get("$ref","").split("/")[-1] or f"{method.upper()} {path}"
        for name,_ in resolve(sch):
            wanted[label].add(name)

# What the web app mentions anywhere outside generated code.
src = subprocess.run(
    ["bash","-c",
     "grep -rho --exclude-dir=gen \"[a-z_][a-z0-9_]*\" %s/web/src --include=*.tsx --include=*.ts "
     "| sort -u" % root],
    capture_output=True, text=True).stdout.split("\n")
mentioned = set(src)

missing = {}
for label, fields in sorted(wanted.items()):
    gone = sorted(f for f in fields if f not in mentioned)
    if gone: missing[label] = gone

total = sum(len(v) for v in wanted.values())
gone  = sum(len(v) for v in missing.values())
print(f"{total} request fields across {len(wanted)} bodies; {gone} never mentioned in web/src\n")
for label, fields in missing.items():
    print(f"  {label}")
    for f in fields: print(f"      {f}")
