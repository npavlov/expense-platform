# Operations

Commands assume the repository root and the local stand (context
`docker-desktop`, namespace `expense-platform-local`). The same scripts work
against AWS with `KUBE_CONTEXT=expense-platform-aws NS=expense-platform`
once `scripts/aws/kubeconfig.sh` has run and the tunnel is open.

## Everyday

| Task | Command |
|---|---|
| Deploy or update (rebuilds images, keeps data) | `make local-up` (`OBSERVABILITY=1` adds Prometheus) |
| State of pods, jobs, volumes, endpoints | `make local-status` |
| Re-open the loopback ports after a Mac or Docker restart | `make port-forward` |
| Outbox backlog | `make outbox-status` |
| Follow one request through both services | `make trace REQUEST_ID=<id>` |
| Logs | `kubectl --context docker-desktop -n expense-platform-local logs deploy/expenses -f` |

## Stop, start, destroy

| | What happens | Data |
|---|---|---|
| `make local-stop` | port-forwards stopped, every Deployment and PostgreSQL scaled to 0 | kept (volume, secrets) |
| `make local-start` | PostgreSQL, then services back to 1 replica, port-forwards, status | as before |
| `make local-up` | rebuild images, re-run setup jobs (idempotent), roll out | kept |
| `make local-destroy CONFIRM=delete-local-data` | **deletes the namespace with the PostgreSQL volume and all secrets** | gone (take `make backup` first) |

Quitting Docker Desktop also stops the stand; after it starts again, pods come
back on their own and only `make port-forward` is needed.

## What stays on the Mac

After `make local-stop` the following still exists; after
`make local-destroy` everything except the last four groups is gone.

| Item | Where | Remove with |
|---|---|---|
| Namespace `expense-platform-local` with Secrets and the PostgreSQL PersistentVolume | Docker Desktop Kubernetes (`local-path` volume inside the Docker Desktop VM) | `make local-destroy CONFIRM=delete-local-data` |
| Port-forward processes (only while forwarded) | background `kubectl` loops, PIDs in `.local/port-forward/` | `make port-forward-stop` |
| Images `expense-platform/{expenses,audit,devidp}` (one tag per build, plus `:build`, `:scan`) | Docker image store | `make clean-images` |
| Pulled tool and base images (postgres, busybox, prometheus, terraform, tflint, trivy, gitleaks, actionlint, redocly, golang, distroless) | Docker image store | `docker image prune -a` (also removes unrelated unused images) |
| Go toolchain 1.26.11, modules, `go run` tool builds (govulncheck, gosec, kubeconform, kind) | `~/go/pkg/mod`, `~/Library/Caches/go-build` | `go clean -modcache -cache` |
| Generated state: rendered kustomizations, port-forward logs, scan output | `.local/` (git-ignored) | `rm -rf .local` |
| Database dumps | `backups/` (git-ignored, mode 600) | delete when no longer needed |
| Terraform provider cache | `infra/terraform/aws/.terraform/` (git-ignored) | `rm -rf infra/terraform/aws/.terraform` |

Nothing is installed system-wide and no launch agent is created. The older
`expenses-lab` namespace is never read or modified by any script: every
script passes `--context` and `-n` explicitly and refuses other namespaces.

## Alerts and runbooks

### OutboxDeliveryStalled

`max(outbox_oldest_undelivered_age_seconds) > 30` for 15 s. Decisions are
being made, but the audit history is not catching up.

1. `make outbox-status`: is `last_error` a timeout/connection error (audit
   down) or an HTTP status?
2. `kubectl … get pods -l app.kubernetes.io/name=audit`; check readiness and logs.
3. Audit down → fix or restart it; the backlog drains on its own (watch
   `outbox_events_pending` go to 0).
4. Audit up but 503 → its database access fails; check PostgreSQL and `audit_app`.

### OutboxEventsParked

`outbox_events_parked > 0`: an event was rejected permanently (4xx) or ran out
of attempts. It will not be retried automatically.

1. `scripts/outbox.sh parked` and read `last_error`.
2. `401` → signing key mismatch: compare `outbox-signing-key` with
   `audit-ingest-keys` (see rotation below). `422` → contract mismatch
   between versions; deploy a compatible audit first. `409` → same event ID
   with different content: a bug; keep the row for analysis.
3. After the cause is fixed: `scripts/outbox.sh requeue <event_id>` (resets the
   attempt budget). The receiver deduplicates, so requeueing is safe.

### ApiErrorRateHigh

More than 5% of public requests end in 5xx for 5 minutes. Check `/ready` of
both services and PostgreSQL; logs contain `request failed` with the request ID.

### AuditIngestRejectedSignatures

`audit` rejected events with a bad signature. Either a key rotation is half
done (see below) or something other than expenses is posting to 8081, which
the NetworkPolicy should prevent: run `make verify-cluster` and check recent
pod creations in the namespace.

## Rotate secrets

All secrets are created by `scripts/secrets.sh` only if missing.

**Event signing key, without downtime:** `scripts/rotate-signing-key.sh`

1. `audit-ingest-keys` gets the old and the new key; audit restarts.
2. `outbox-signing-key` gets the new key and key ID; expenses restarts.
3. Once the outbox is drained (pending and parked = 0), the old key is removed
   from audit.

The key ID lives in the same Secret as the key, so a later `make local-up`
keeps the rotated pair. Drilled locally (k1 → k2 → k3, e2e green afterwards,
no event rejected or parked). Port-forwards reconnect about 2 s after each
restart.

**Database role password** (short outage of the affected service):
replace the value in `db-role-passwords`, run `make local-up` (the bootstrap
job sets the new password and `secrets.sh` regenerates the connection URLs),
then `kubectl rollout restart deployment/<service>`.

A leaked secret counts as compromised: rotate first, then look for its use in
the logs.

## Backup and restore

| | Command | Covers |
|---|---|---|
| Logical backup | `make backup` → `backups/expense_platform-<UTC>.dump` + `.sha256` | both schemas, one consistent snapshot (`pg_dump`) |
| Restore drill | `make restore-check` (newest dump; `CLAIM_ID=` to pick a claim) | restores into a **separate, clean** PostgreSQL container, reads one decided claim with its history, compares with the live stand, times the restore against `RTO_TARGET_SECONDS` (default 1800) |
| AWS volume snapshots | DLM, daily 03:00 UTC, last 3 kept | whole data volume (k3s state, DB, certificates), crash-consistent |

Targets for this stand: RPO 24 h (daily snapshot; take `make backup` before a
risky change), RTO 30 min. The drill measured 2 s locally for a small dump.

**Restoring into the live stand** (designed, not drilled): stop writers, then
restore over the existing schemas as superuser and restart.

```bash
K="kubectl --context docker-desktop -n expense-platform-local"
$K scale deployment/expenses deployment/audit --replicas=0
$K exec -i postgres-0 -- pg_restore -U postgres -d expense_platform --clean --if-exists --exit-on-error < backups/<file>.dump
make local-up   # re-applies role grants and starts the services
```

## Local recovery scenarios

| Symptom | Action |
|---|---|
| A pod crash-loops | `kubectl … describe pod`, `logs --previous`; readiness shows dependency problems |
| Setup job failed (`make local-up` times out at "Wait for database setup") | `kubectl … logs job/db-bootstrap` (or `*-migrate`); fix and re-run `make local-up`, jobs are recreated |
| `127.0.0.1:3008x` refuses connections | `make port-forward` |
| Docker Desktop Kubernetes was reset | the namespace and volume are gone: `make local-up`, then restore the latest dump as above |
| `make verify-cluster` fails in both directions (allowed flows blocked, denied flows allowed) and kindnet logs `failed to set verdict` | the CNI's policy controller is degraded, not the policies (see [verification.md](verification.md#finding-kindnets-policy-controller-degraded-under-pod-churn)). Restart it: `kubectl --context docker-desktop -n kube-system rollout restart daemonset/kindnet` (affects every namespace on the cluster briefly), or restart Kubernetes in Docker Desktop |
