# Learning path

Ten stages, each building on the previous one. For every stage: the goal,
the files to open, a command to run yourself, what you should see, and one
question to answer without looking. Plan about an hour per stage.

Before you start: `make local-up` (first run builds images, about 2 minutes)
and `make demo` once, so you know what the finished system does.

---

## 1. Go: one HTTP service, tested in isolation

**Goal.** Read how a request becomes a response, and how the rules are tested
without a database.

**Open.**
`services/expenses/internal/httpapi/router.go` (routes, one handler per operation),
`services/expenses/internal/claims/claims.go` (rules, no I/O),
`libs/platform/httpx/httpx.go` (`Wrap`, `DecodeJSON`, `WriteProblem`),
`services/expenses/internal/httpapi/claims_test.go` (in-memory store, fake tokens).

**Run.**
```bash
cd services/expenses && go test -race -run 'TestSubmitInputBoundaries|TestAuthorizationMatrix' -v ./internal/httpapi/
```

**See.** Every row of the input-boundary table (zero amount, `"status":"approved"`
in the body, a 5 KB body…) and every caller × route pair of the authorization
matrix passes, in milliseconds, without Docker.

**Question.** Why does `GET /v1/claims/{id}` answer 404 rather than 403 when
Bob asks for Alice's claim, and where exactly is that decided?

---

## 2. PostgreSQL: the database enforces the rules too

**Goal.** See which guarantees live in the schema, and why the service role
cannot break them.

**Open.**
`services/expenses/migrations/001_create_claims.sql` … `004_grant_app_role.sql`,
`deploy/k8s/base/db/bootstrap.sql` (roles and schemas),
`services/expenses/internal/storage/store_integration_test.go` (`TestDatabaseEnforcesRules`, `TestRuntimeRoleIsolation`).

**Run.**
```bash
make test-integration
```

**See.** A throwaway PostgreSQL starts, the roles are created, both services'
migrations are applied by their migrator roles, and the tests connect as the
runtime roles: changing an approved amount fails with `42501`, self-approval
with `23514 claims_no_self_decision`.

**Question.** `expenses_app` may `UPDATE` claims. Which columns, and which
attack does the column list prevent?

---

## 3. Transactions: idempotency, races and the outbox row

**Goal.** Understand the three places where a transaction makes a guarantee.

**Open.**
`services/expenses/internal/storage/claims.go` (`Create`, `Decide`),
`services/expenses/migrations/002_claim_decisions_and_idempotency.sql` (deferred foreign key),
`docs/architecture.md#consistency-model`.

**Run.**
```bash
cd services/expenses && GOTEST_FLAGS=-v ../../scripts/test-integration.sh 2>&1 | grep -E 'Concurrent|Idempotent'
```

**See.** `TestConcurrentCreateWithSameKey` (20 parallel submissions with one
key → one claim) and `TestConcurrentDecisions` (40 parallel approvals and
rejections → exactly one decision and one outbox event) pass.

**Question.** Why is the idempotency key inserted *before* the claim, and what
would go wrong if the order were reversed without the deferred foreign key?

---

## 4. Second service: audit and its contract

**Goal.** See how two services agree on an event without sharing code or tables.

**Open.**
`contracts/events/README.md` and `expense-decided.v1.schema.json`,
`services/audit/internal/events/events.go`,
`services/expenses/internal/claims/contract_test.go`,
`services/audit/internal/httpapi/httpapi.go` (two routers, two trust levels).

**Run.**
```bash
cd services/audit && go test -v -run 'TestContractExamples|TestIngest' ./internal/...
```

**See.** The v1 example and an additive v1 change are accepted, the v2 example
is refused as `unsupported_schema_version`; a redelivered event gets `200
duplicate`, a tampered one with the same ID `409`.

**Question.** A colleague wants to rename `amount_cents` to `amount`. What do
you do, in which order do the services ship, and which test fails if you skip
a step?

---

## 5. Delivery failure: stop audit, watch the history catch up

**Goal.** Experience the problem the outbox solves: the first write succeeded,
the second service is down.

**Open.**
`services/expenses/internal/outbox/outbox.go` (lease, backoff, park),
`services/expenses/internal/storage/outbox.go` (`Lease` with `SKIP LOCKED`),
`services/expenses/internal/storage/outbox_integration_test.go` (lost acknowledgement).

**Run.**
```bash
kubectl --context docker-desktop -n expense-platform-local scale deployment/audit --replicas=0
make demo        # or decide a claim by hand, then:
make outbox-status
kubectl --context docker-desktop -n expense-platform-local scale deployment/audit --replicas=1
make outbox-status
```

**See.** While audit is down the decision still returns `approved` at once,
the outbox shows one pending event with growing `attempts` and
`last_error: timeout`; after audit returns, `pending` drops to 0 and the
history has exactly one entry.

**Question.** The worker delivered the event, audit stored it, and the
response was lost. What happens next, and which two mechanisms together make
the outcome correct?

---

## 6. Kubernetes: what runs where

**Goal.** Map every object in the cluster to its purpose.

**Open.**
`deploy/k8s/base/kustomization.yaml` and everything it lists,
`deploy/k8s/overlays/local/kustomization.yaml`, `deploy/k8s/overlays/aws/kustomization.yaml`,
`scripts/deploy-local.sh`, `scripts/secrets.sh`.

**Run.**
```bash
kubectl kustomize deploy/k8s/overlays/local | grep -E '^kind:' | sort | uniq -c
make local-status
```

**See.** Deployments, a StatefulSet with a PersistentVolumeClaim, three
completed setup Jobs, ClusterIP Services only, ten NetworkPolicies, no Secret
in the rendered YAML.

**Question.** Why are the migrations Jobs instead of init containers of the
service Deployments? Think about which credentials each pod holds.

---

## 7. Security: try to break it

**Goal.** Check the controls from the outside, as an attacker inside the
cluster and as a user with the wrong token.

**Open.**
`docs/threat-model.md` (pick three threats), `scripts/verify-cluster.sh`,
`libs/platform/authn/authn.go`, `tests/e2e/e2e_test.go` (`TestForgedAndMissingTokensAreRejected`).

**Run.**
```bash
make verify-cluster
make e2e
```

**See.** Root, privileged and hostPath pods are rejected; an unlabelled pod
cannot reach PostgreSQL, audit's 8081, the internet or a metadata address,
while the positive controls connect; a token signed with another key is 401.

**Question.** Anyone who can create a pod labelled `app.kubernetes.io/name=expenses`
passes the NetworkPolicy on port 8081. Which control still stops them from
writing a forged decision into the history?

---

## 8. Observability: one decision, two services, one alert

**Goal.** Follow a request through the logs and see an alert fire for the
outage from stage 5.

**Open.**
`libs/platform/httpx/httpx.go` (`Wrap`: allowlisted log fields, route-pattern
metric labels), `deploy/k8s/overlays/local-observability/alerts.yml`,
`docs/operations.md#alerts-and-runbooks`.

**Run.**
```bash
make local-up OBSERVABILITY=1
make obs-demo
make trace REQUEST_ID=<an X-Request-ID printed by make demo>
```

**See.** `outbox_oldest_undelivered_age_seconds` climbs, the alert goes
`inactive → pending → firing`, then resolves after audit returns. The trace
shows the same request ID in expenses (`claim decided`, `delivery failed`,
`event delivered`) and in audit (`decision recorded`). Open the graph URL the
script prints.

**Question.** Why is the claim ID allowed in logs but not as a metric label?

---

## 9. Terraform: read the plan before anything exists

**Goal.** Connect each resource to the architecture without spending money.

**Open.**
`infra/terraform/aws/*.tf`, `infra/terraform/aws/templates/cloud-init.yaml.tftpl`,
`docs/aws.md`, `docs/cost.md`.

**Run.**
```bash
make tf-fmt tf-validate tf-lint tf-scan
```

**See.** Formatting, validation and lint are clean; the scanner reports
nothing above the one documented exception (node egress) in
`.trivyignore.yaml`.

**Question.** Which three settings together stop a compromised pod on the
EC2 node from obtaining the instance's AWS credentials?

---

## 10. Publishing to AWS (your turn)

**Goal.** Run the stand in AWS yourself, verify it from the outside, and
remove it again.

**Open.** `docs/aws.md` from top to bottom, `.github/workflows/release.yml`,
`scripts/aws/*.sh`.

**Run.** Follow [aws.md](aws.md#steps): publish images, `plan` (read it and
map each resource), `apply`, find the instance, volumes, security group and
user pool in the console, `scripts/aws/deploy.sh`, the outside checks,
`scripts/aws/e2e.sh`, the restore drill, then `plan -destroy`, `apply` and the
snapshot cleanup.

**See.** `https://<ip>.sslip.io/health` with a valid certificate, 401 without
a token, closed 22/6443/5432, the e2e suite green with Cognito tokens, and no
billable resource left the next day.

**Question.** You replaced the instance with `-replace=aws_instance.node`.
Why are the claims and the TLS certificate still there, and what would you
lose if the data volume were deleted instead?
