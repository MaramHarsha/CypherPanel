# Skills in this repo

Two kinds of skills live here:

- **CypherPanel-specific** (`reconciler-development/`, `verify/`) — written for
  this codebase; see [project-structure.md](../../docs/project-structure.md).
- **Imported from Anthropic's public skill repos**, brought in for future use
  even where not yet exercised by this project:
  - `threat-model/`, `vuln-scan/`, `triage/`, `patch/`, `_lib/checkpoint.py` —
    from [defending-code-reference-harness](https://github.com/anthropics/defending-code-reference-harness).
    These four are read/write-only (no target-code execution) and are
    self-contained; the harness's execution-dependent skills (`customize`,
    `dnr-hunt`, `dnr-respond`) and its autonomous ASAN pipeline were left out
    because they need the harness's own infrastructure (`harness/`,
    `dnr_harness/`, `targets/`) to function, and that pipeline targets C/C++
    memory-corruption bugs — a bug class Go doesn't have. `vuln-scan` (general
    static security review) is the closest fit for auditing this codebase.
  - `frontend-design/`, `webapp-testing/`, `mcp-builder/`, `skill-creator/`,
    `claude-api/` — from
    [anthropics/skills](https://github.com/anthropics/skills). Office-document
    and creative-asset skills from that repo (docx, pptx, xlsx, pdf,
    canvas-design, etc.) were left out — no product surface for them in a
    self-hosted deployment PaaS.

  Each imported skill directory carries its own `LICENSE.txt` from its source
  repo. A few "see also" links inside these skills point at docs that live in
  the source repos, not here (e.g. `docs/security.md`) — informational only,
  not a functional dependency.
