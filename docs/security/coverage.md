# Security coverage

The project was checked against twelve study topics on application and
platform security (the author's private notes; not part of this repository).
For each topic: what applies to this system, where it is implemented, how it
is verified, and what is deliberately left out.

✅ implemented and tested · 🟡 partial / configured but not verified here · ⏭ skipped with a reason

| # | Topic | Applied here | Status | Evidence |
|---|---|---|---|---|
| 1 | Threat modeling: assets, trust boundaries, STRIDE, risk decisions | DFD, 7 boundaries, 25 threats with decisions, residual-risk register | ✅ | [threat-model.md](../threat-model.md) |
| 2 | Authentication and authorization | OIDC access tokens (RS256, iss, exp, token_use, client_id), deny-by-default per route, object ownership, roles from token only | ✅ | `libs/platform/authn`, `TestAuthorizationMatrix`, `TestObjectLevelAccess`, e2e |
| 2 | — passwords, MFA, sessions/cookies | delegated to Cognito; no passwords or cookies in the services | 🟡 | MFA optional in `cognito.tf` (R10); no browser UI, so no cookies/CSRF |
| 2 | — service identity | HMAC with key IDs and rotation; NetworkPolicy by source pod; locally Istio mTLS (SPIFFE IDs per ServiceAccount) with STRICT mode and an identity-based AuthorizationPolicy on audit's 8081 | ✅ local / 🟡 AWS | `verify-cluster.sh` mesh checks; AWS keeps the HMAC key only (R2) |
| 2 | — tenant isolation | per-employee isolation in one table, filter in one data-access layer, negative tests | ✅ | `claims.CanView`, `ListByEmployee`, `TestObjectLevelAccess` |
| 3 | Secure code: injection, XSS, SSRF, uploads | parameterized SQL only; JSON API only (no HTML rendering); outbound URLs are configuration, redirects not followed; no file uploads | ✅ | `internal/storage`, `TestHTTPDelivererClassifiesResponses` (redirect to IMDS not followed), gosec |
| 3 | — mass assignment | unknown JSON fields rejected | ✅ | `TestSubmitInputBoundaries` |
| 4 | Financial business logic | idempotency keys with hash and scope, atomic state transition, DB constraints, four-eyes rule, integer minor units, audit trail bound to the amount | ✅ | `TestConcurrentDecisions`, `TestConcurrentCreateWithSameKey`, `TestDatabaseEnforcesRules` |
| 4 | — webhook-style verification | HMAC over raw bytes, constant-time compare, timestamp window, event-ID dedup, ordering-independent | ✅ | `sigauth_test.go`, `TestIngest*` |
| 4 | — velocity limits, reconciliation | not implemented; the outbox-age alert is the detective control between the two services | ⏭ | R9; `OutboxDeliveryStalled` |
| 5 | Secrets and sensitive data | secrets generated at deploy time, mounted as files (0440, pod group), never in Git or images; per-workload secrets; rotation procedure; log allowlist; data minimization (description not in events) | ✅ | `scripts/secrets.sh`, `TestClusterBoundaries`, `TestEventMatchesContract`, [operations.md](../operations.md#rotate-secrets) |
| 5 | — encryption | EBS encrypted, k3s secrets encryption (AWS), TLS at the edge; no in-cluster TLS, no field-level encryption | 🟡 | R3; synthetic data only |
| 5 | — retention | not defined for a demo | ⏭ | would be required for real personal data |
| 6 | SAST and security review | gosec (two reviewed exceptions), custom-rule-like checks as tests (manifest policies), CODEOWNERS for sensitive paths | ✅ | `make sast`, `tests/manifests`, `.github/CODEOWNERS` |
| 7 | Dependencies and supply chain | go.sum, govulncheck (found and fixed reachable stdlib issues: Go 1.26.3 → 1.26.11), Dependabot with 7-day cooldown, actions pinned by SHA, images by digest, SBOM | ✅ | `make vuln`, CI `images` job, `.github/dependabot.yml` |
| 8 | Container images and CI/CD | multi-stage, distroless nonroot, no shell; trivy (fixable HIGH/CRITICAL fail the build); read-only token in CI, no secrets for PRs; keyless signing + provenance in release | ✅ / 🟡 | `make image-scan`; signing configured, not executed (no publishing) |
| 8 | — admission verification of signatures | not enforced | ⏭ | R4 |
| 9 | Cloud and Kubernetes hardening | minimal IAM (Session Manager only), IMDSv2 hop 1, no SSH/API ports; Pod Security restricted, no SA tokens, no RBAC grants, default-deny NetworkPolicy verified on an enforcing CNI | ✅ / 🟡 | `verify-cluster.sh`; AWS parts validated offline only |
| 9 | — GitOps / Argo CD, org policies | single operator, manual apply; no AWS Organization in scope | ⏭ | documented in [aws.md](../aws.md) |
| 10 | DAST and API testing | negative tests: authorization matrix, BOLA, mass assignment, input bounds, forged tokens, response field allowlist; fuzzing of parsers | ✅ | `TestAuthorizationMatrix`, `TestSubmitAndRead` (exact fields), `make fuzz` |
| 10 | — scanner-based DAST (ZAP) | OpenAPI specs exist for it; not run | ⏭ | `contracts/http/*.openapi.yaml` |
| 11 | Production operations | structured logs with request IDs across services, append-only audit trail, k3s API audit log (AWS), alerts with runbooks, backup + timed restore drill, DLM snapshots | ✅ / 🟡 | `make obs-demo`, `make restore-check`, [operations.md](../operations.md) |
| 11 | — immutable off-site backups, incident response process | not for a demo | ⏭ | R1, R8 |
| 12 | Security as engineering practice | owners (CODEOWNERS), release gates in CI that fail on error, exception register with expiry, threat model kept with the code | ✅ | [exceptions.md](exceptions.md), `.github/workflows/ci.yml` |
| 12 | — metrics, SLAs, 90-day plan | organisational, out of scope for one repository | ⏭ | — |
