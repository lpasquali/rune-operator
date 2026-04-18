# Testing — rune-operator

Full docs live in [rune-docs](https://github.com/lpasquali/rune-docs) (Developer
Guide, E2E Testing spec). This file only carries repo-local testing details
that would be lossy to keep only in rune-docs.

## Running the suite

```bash
# Full unit suite (~80s, cache-warm)
go test ./... -count=1

# With coverage (CI gate: 99.5% floor)
go test ./... -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out   # check "total" line

# Lint + SAST
test -z "$(gofmt -l .)"
go vet ./...
gosec -fmt json -severity high ./...

# CVE scan (required before any go.mod change)
govulncheck ./...
```

## Peak RSS — approved exception to the 512 MiB planning guideline

The [test & coverage inventory epic](https://github.com/lpasquali/rune-docs/issues/249)
proposed a **512 MiB peak RSS** planning guideline for Go test suites. This
repo's envtest-free, fake-client-based tests currently run at:

| Package | Peak RSS (MiB, cache-warm) |
|---|---|
| root `github.com/lpasquali/rune-operator` | ~640 |
| `controllers` | ~620 |
| `internal/metrics` | ~560 |
| `internal/telemetry` | ~220 |
| `api/v1alpha1` | ~180 |

(ARM64 macOS / Linux; x86-64 Linux runners measured up to ~732 MiB in the
[#97 audit](https://github.com/lpasquali/rune-operator/issues/97).)

### Root cause

The floor is set by the **`sigs.k8s.io/controller-runtime` v0.23 +
`k8s.io/client-go` import graph**, not by anything the test files do:

- `main.go` imports `controllers`, `internal/metrics`, and
  `internal/telemetry`; any `go test` that compiles the root package links in
  the full controller-runtime manager, zap logger, and healthz server.
- `controllers` uses `sigs.k8s.io/controller-runtime/pkg/client/fake` —
  lightweight at runtime, but the import still pulls in `k8s.io/apimachinery`
  + `k8s.io/client-go/kubernetes/scheme` + `k8s.io/apiextensions-apiserver`
  for decoding. Reflection-based scheme building is the dominant cost.
- `internal/metrics` is a small file, but it imports
  `sigs.k8s.io/controller-runtime/pkg/metrics` to register Prometheus
  metrics with the controller-runtime registry — correct by design, but
  costs ~500 MiB in the test binary.

### Why the floor can't realistically drop below ~600 MiB

Options explored and rejected:

| Option | Outcome |
|---|---|
| `go test -p 1` (sequential package builds) | Slight **regression** (632 → 676 MiB); Go holds cache state longer |
| Per-package `go test` invocations in sequence | Peak is still bounded by the largest single package binary (~640 MiB root / ~620 MiB controllers); no wall-clock win either |
| Strip `ctrlmetrics` import from `internal/metrics` | Breaks the controller's own Prometheus registration; not a lossless change |
| Split `controllers` into sub-packages (`controllers/budget`, `controllers/estop`, etc.) | Marginal gain — every sub-package still imports controller-runtime client/fake, so each test binary still carries the same floor |
| Remove envtest | Already removed; the tests are fake-client + `httptest` only |

### CI implications

- The CI runner (Ubuntu 24.04 GitHub-hosted) has 7 GiB RAM. The 512 MiB figure
  was a **planning guideline**, never a hard CI gate for this repo, and the
  tests have never OOM-killed on the runner.
- If a future project-wide rollout lowers the hard ceiling below 700 MiB,
  the actionable path is **CI matrix sharding** in the `rune-ci`
  `go-quality.yml` reusable workflow (one job per top-level package), not
  per-repo surgery. That shifts the bound from "sum of package peaks" to
  "max single package peak" on the per-job bill, but the test wall-clock
  grows linearly.

### Local tuning for developers

If you're testing on a memory-constrained workstation:

```bash
# Single package at a time; no speed loss vs ./... on cold cache
go test ./api/v1alpha1 -count=1 && \
  go test ./internal/telemetry -count=1 && \
  go test ./internal/metrics -count=1 && \
  go test ./controllers -count=1 && \
  go test . -count=1
```

Each invocation runs in its own process; peak RSS is bounded by the
largest single package (~640 MiB) rather than Go's scheduler holding all
compiled test binaries open at once.

## Issue closure

[#111](https://github.com/lpasquali/rune-operator/issues/111) is closed by
this documentation under the "document an approved exception" option in the
issue's goal list. The RSS floor is well understood, documented, and
inherent to the framework — not a bug in RUNE's test layout.
