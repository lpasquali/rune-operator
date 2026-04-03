# rune-operator

Standalone Go repository for the RUNE Kubernetes operator.

This repository contains the CRD, controller logic, metrics, tracing hooks, container build, and CI/security gates for the operator independently of the main application repo.

## Features

- CRD: `RuneBenchmark` (`bench.rune.ai/v1alpha1`)
- Schedules and triggers benchmark jobs against RUNE API
- Status history, conditions, failure counters
- Prometheus metrics on `/metrics`
- OpenTelemetry trace emission (OTLP) when configured
- Panic recovery with stack traces logged for ingestion by observability backends

## CRD

CRD manifest:
- `config/crd/bases/bench.rune.ai_runebenchmarks.yaml`

Sample:
- `config/samples/bench_v1alpha1_runebenchmark.yaml`

## Metrics

- `rune_operator_reconcile_total{result=...}`
- `rune_operator_run_duration_millis`
- `rune_operator_active_schedules`

## Build

```bash
go mod tidy
go build ./...
```

## Build Operator Container Image

```bash
docker build -t rune-operator:local .
```

## Run locally

```bash
go run . --leader-elect=false
```

## Deploy outline

1. Apply CRD.
2. Create RBAC + ServiceAccount.
3. Run deployment for this operator.
4. Create `RuneBenchmark` resources.

The operator calls the RUNE API endpoint `/v1/jobs` using `spec.apiBaseUrl`.

## Quality Gates

This repository is designed to preserve the same core protections as the original monorepo split source:

- Go test and coverage enforcement
- formatting and `go vet` checks
- container smoke build
- SBOM generation and CVE policy enforcement
- SAST and dependency/license scanning
- required merge gate workflow aggregation
