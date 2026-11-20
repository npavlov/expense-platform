# Verification log

What was actually run, where, and with which result. Last run: **2026-11-20**,
macOS (Apple silicon), Docker Desktop with Kubernetes v1.36.1 (kind-based
provisioning, kindnet CNI), Go 1.26.11.

## Offline checks (`make check`, 91 s, exit 0)

| Check | Result |
|---|---|
| `fmt-check`, `vet` (incl. `integration`, `e2e` tags) | clean, 5 modules |
| `test` (race detector) | all packages pass |
| `test-integration` (PostgreSQL 17.12, production roles) | 27 integration and unit tests pass in `expenses`, 9 in `audit` |
| `vuln` (govulncheck v1.10.0) | no vulnerabilities in 5 modules. The first run found 20 reachable standard-library issues in Go 1.26.3; fixed by moving to Go 1.26.11 |
| `sast` (gosec v2.30.0) | 0 issues; 2 reviewed `#nosec` (EX-1, EX-2) and one real fix (integer conversion in the idempotency hash) |
| `k8s-lint` (kubeconform 0.8.0, strict) + `tests/manifests` | local 32, observability 37, aws 27 resources valid; policy tests pass |
| `secrets-scan` (gitleaks 8.29.0, full history) | no leaks |
| `api-lint` (Redocly 2.11.1) | both OpenAPI documents valid |
| `gha-lint` (actionlint 1.7.8) | clean |
| `tf-fmt`, `tf-validate` (Terraform 1.14.0, AWS provider 6.73.0), `tf-lint` (tflint 0.60.0, AWS ruleset 0.44.0) | clean |
| `tf-scan` (trivy 0.67.2, HIGH/CRITICAL) | clean with two documented exceptions (EX-3, EX-4) |
| `fuzz` (10 s per target) | ~9 million inputs, no failures (`sigauth.Verify`, `httpx.DecodeJSON`, `events.Parse`) |
| `image-scan` (trivy, fixable HIGH/CRITICAL) | 0 findings in all three images |

Mutation checks (temporarily breaking the code or config) confirmed that the
tests catch what they claim: the integration test fails when the amount check
is weakened to `>= 0`; the manifest tests fail on a writable root filesystem,
a NodePort Service and an `http://` issuer in the AWS overlay.

## Local stand (Docker Desktop, namespace `expense-platform-local`)

| Step | Result |
|---|---|
| `make local-destroy` then `make local-up` from scratch | ready in 19 s (images cached); the `expenses-lab` namespace untouched |
| `make e2e` (with `E2E_DISRUPTIVE=1`) | 8 tests pass; audit outage recovered in 16-27 s |
| `make demo` | all six steps as described; decision during the outage delivered 8 s later |
| `make obs-demo` (observability profile) | alert `inactive → pending (36 s) → firing (51 s)`, resolved after audit returned |
| `make backup` + `make restore-check` | restored into a clean PostgreSQL in 1-2 s; claim and history match live; live data untouched |
| `scripts/rotate-signing-key.sh` | rotated k1 → k2 → k3 with no rejected or parked event; e2e green after a redeploy |
| `make verify-cluster` | 17/17 checks on earlier runs; see the finding below |

### Finding: kindnet's policy controller degraded under pod churn

After roughly two hours and many pod restarts (e2e, demos, probes), kindnet's
network-policy controller on the Docker Desktop cluster started logging
`failed to set verdict … netlink send: i/o timeout` with 9-second nftables
syncs, and `verify-cluster` failed 5 of 14 network checks **in both
directions** (allowed flows blocked, internet egress allowed) for newly
started pods. The running services were unaffected. The same manifests and
script then passed **all 17 checks**, also after the e2e suite and the demo, in
a fresh, separate kind cluster (`kind v0.33.0`, Kubernetes 1.37, the CI path
`KIND_CLUSTER=… scripts/deploy-local.sh`), which was deleted afterwards.

Conclusion: a fault of the local CNI component, not of the policies. Recovery
on the Docker Desktop cluster would be restarting the kindnet pod (or
Kubernetes in Docker Desktop); that was not done because the cluster also runs
another, unrelated namespace. Lesson recorded in the runbook: network policy
enforcement is a dependency that needs its own health check; `verify-cluster`
is that check and runs in CI.

## Separate kind cluster (CI path)

| Step | Result |
|---|---|
| `kind create cluster` + `scripts/deploy-local.sh` with `KIND_CLUSTER` | ready in 32 s |
| `verify-cluster` | 17/17 (3 Pod Security, 14 network checks with positive controls) |
| e2e | 8 tests pass; outage recovery 9 s |
| demo | history caught up 2 s after audit became ready |
| backup + restore drill | pass, 1 s |
| `verify-cluster` again after the load | 17/17 |

## Not executed

| What | Why |
|---|---|
| GitHub Actions runs of `ci.yml` and `release.yml` | no push and no GitHub settings changes were allowed; the workflows are linted (actionlint) and every `make` target they call was run locally, including the kind-based e2e job |
| Image publishing, signing, provenance | no publishing to external registries |
| `terraform plan/apply/destroy`, anything in AWS | AWS actions are left to the owner; see [aws.md](aws.md#verified-locally-vs-needs-aws) for what only AWS can confirm |
| `scripts/aws/*.sh` | they need the AWS stand; syntax checked with `bash -n` only |
| Restoring a dump into the live stand | procedure documented in [operations.md](operations.md#backup-and-restore); the drill restores into a separate database |
