## Summary

<!-- 1-3 sentences: what and why -->

Closes #<!-- issue number -->
Epic: #<!-- parent epic number -->

## DoD Level

<!-- Check ONE. See https://github.com/lpasquali/rune-docs/blob/main/docs/context/SYSTEM_PROMPT.md -->

- [ ] **Level 1 — Full Validation** (runtime, API, Helm, Dockerfile)
- [ ] **Level 2 — Test Infrastructure** (test config, CI, coverage, linter)
- [ ] **Level 3 — Documentation** (Markdown, MkDocs, diagrams)

## Level 1 Checklist

<!-- Skip this section if Level 2 or 3. All boxes must be checked for Level 1. -->

- [ ] Tested in **docker-compose mode**
- [ ] Tested in **kind (Kubernetes) mode**
- [ ] Tested in **standalone CLI mode**
- [ ] **Breaking change audit**: API versions, persistent data, cross-component contracts
- [ ] **Dependency CVE audit** (if deps changed): `pip-audit` / `govulncheck` / `grype` — no new CVEs

## Level 2 Checklist

<!-- Skip if Level 1 or 3. -->

- [ ] Full test suite passes
- [ ] Coverage not degraded (at or above floor)
- [ ] No unintended CI side effects

## Level 3 Checklist

<!-- Skip if Level 1 or 2. -->

- [ ] `mkdocs build --strict` passes
- [ ] `pymarkdown scan README.md docs` passes

## Audit Checks

<!-- Required when trigger conditions are met. See trigger table in SYSTEM_PROMPT.md. -->
<!-- Delete rows that don't apply. If none apply, write "No triggers fired." -->

| Check | Result | Evidence |
|---|---|---|
| `legal check:dep <pkg>` | PASS / FAIL | <!-- link or paste --> |
| `legal check:integration <agent>` | PASS / FAIL | <!-- link or paste --> |
| `legal check:tool <tool>` | PASS / FAIL | <!-- link or paste --> |
| `cyber check:dep <pkg>` | PASS / FAIL | <!-- link or paste --> |
| `cyber check:api` | PASS / FAIL | <!-- link or paste --> |
| `cyber check:supply-chain` | PASS / FAIL | <!-- link or paste --> |
| `cyber check:vex` | PASS / FAIL | <!-- link or paste --> |

## Acceptance Criteria Evidence

<!-- For each acceptance criterion from the issue, tick the box and attach evidence. -->
<!-- CI-produced evidence (green checks) counts automatically. -->
<!-- For everything else: screenshots, log snippets, before/after diffs, command output. -->

- [ ] <!-- criterion 1 — evidence: ... -->
- [ ] <!-- criterion 2 — evidence: ... -->

## Test Plan Evidence

<!-- Attach required evidence per the issue's test plan section. -->

- [ ] <!-- evidence item 1 -->
- [ ] <!-- evidence item 2 -->

## Breaking Changes

<!-- Describe any backward-incompatible changes. If none, write "None." -->

## Notes for Reviewer

<!-- Anything the reviewer should know: edge cases, decisions made, open questions. -->
