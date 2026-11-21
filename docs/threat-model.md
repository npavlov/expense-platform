# Threat model

Scope: the expense approval flow (submit → decide → audit history) on the
local stand and the single-node AWS stand. Method: data-flow diagram, STRIDE
per trust boundary, likelihood × impact, one decision per threat. Reviewed
2026-11-20; update when a boundary, data type or integration changes.

## Assets

| Asset | Property that matters most | Why |
|---|---|---|
| Claim amounts and decisions | **Integrity** | a forged or doubled approval pays out money |
| Decision history | Integrity, non-repudiation | proves who approved what and when |
| Claim descriptions, employee IDs | Confidentiality | personal and financial data (synthetic in this project) |
| Signing keys: Cognito/devidp keys, outbox HMAC key | Confidentiality | forge identities or events |
| Database credentials (4 roles + superuser) | Confidentiality | direct data access |
| k3s admin kubeconfig, Session Manager access | Confidentiality | full control of the stand |
| Availability of the API | Availability | lowest priority for a demo, but outages must not corrupt data |

## Actors

- **Employee** (Alice, Bob): legitimate, may be curious or malicious towards
  other employees' data.
- **Reviewer** (Rita, Victor): legitimate, may try to approve their own claim.
- **Internet attacker**: scans the public endpoint, replays requests, sends
  forged tokens.
- **Compromised pod**: an attacker with code execution in one container.
- **Malicious or compromised dependency / CI component**.
- **Operator** with kubectl or AWS access (trusted, but mistakes happen).

## Trust boundaries

| # | Boundary | Crossing | Verified at the crossing |
|---|---|---|---|
| B1 | Internet → ingress | HTTPS requests | TLS (AWS), routing to public ports only |
| B2 | Ingress → `expenses`/`audit` public API | requests with bearer tokens | token signature/issuer/expiry/type/client, group per route, ownership per object |
| B3 | `expenses` → `audit` internal API | outbox events | NetworkPolicy (source pod), HMAC signature + timestamp, schema, event-ID dedup |
| B4 | Services → PostgreSQL | SQL | per-service role, column-level grants, constraints, parameterized queries |
| B5 | Pods → node / cloud | IMDS, Kubernetes API, internet | IMDSv2 hop limit 1, no SA tokens, egress NetworkPolicy |
| B6 | CI → registry → cluster | images | pinned actions, scans, signatures, digest-pinned deploys |
| B7 | Operator → cluster / AWS | kubectl, Session Manager | no open SSH/API port, IAM, short-lived AWS sessions |

## Threats and decisions

L/I = likelihood / impact (low, medium, high). Every "mitigate" names how it
is verified.

| ID | STRIDE | Boundary | Threat | L/I | Decision and controls | Verified by |
|---|---|---|---|---|---|---|
| T1 | S | B2 | Forged or altered token (own key, `alg: none`, HS256 with public key, tampered groups) | M/H | Mitigate: RS256 only, keys from issuer JWKS, exact `iss`, expiry | `authn_test.go` `TestVerifyRejects`; e2e `TestForgedAndMissingTokensAreRejected` |
| T2 | S | B2 | ID token or another app's token used as API token | M/M | Mitigate: `token_use=access`, `client_id` allowlist | `TestVerifyRejects` (id token, wrong client) |
| T3 | E | B2 | Local-only shortcut (http issuer, devidp) reaches AWS | L/H | Mitigate: no auth bypass flag exists; http issuer needs explicit opt-in only set in the local overlay; devidp only in local overlay | `tests/manifests` `TestAWSOverlayHasNoLocalShortcuts`; `TestNewVerifierRequiresHTTPSIssuer` |
| T4 | I | B2 | BOLA/IDOR: Bob reads Alice's claim by ID or via the list | H/H | Mitigate: identity only from token; ownership check in one place (`claims.CanView`); 404 not 403 | `TestObjectLevelAccess`, `TestAuthorizationMatrix`, e2e `TestDemoScenario` |
| T5 | E | B2 | Employee calls reviewer routes | M/H | Mitigate: every route registered through `authn.Require` with explicit groups (deny by default) | `TestAuthorizationMatrix`, e2e `TestRoleChecksOnHistory` |
| T6 | E/T | B2 | Self-approval (four-eyes bypass) | M/H | Mitigate: rule in service + DB constraint `claims_no_self_decision` | `TestCheckDecision`, `TestDatabaseEnforcesRules`, e2e `TestSelfApprovalIsForbidden` |
| T7 | T | B2 | Race: two decisions on one claim, or approval while another reviewer rejects | M/H | Mitigate: row lock, status check, consistency constraint, unique outbox event | `TestConcurrentDecisions` (40 parallel, real DB), e2e `TestConcurrentDecisions` |
| T8 | T | B2 | Retry after timeout creates a second claim | H/M | Mitigate: mandatory `Idempotency-Key`, scoped per employee, request hash | `TestIdempotentSubmit`, `TestConcurrentCreateWithSameKey`, e2e `TestIdempotentRetryOverHTTP` |
| T9 | T | B2 | Mass assignment (`status`, `employee_id` in body), negative/zero/huge amounts, oversized body | H/M | Mitigate: strict decoder (unknown fields rejected), validation, DB checks, 4 KiB limit | `TestSubmitInputBoundaries`, `TestClaimsTableEnforcesRules` |
| T10 | T | B4 | Amount edited after approval | L/H | Mitigate: app role has no UPDATE on amount/description; the event carries the decided amount | `TestDatabaseEnforcesRules` |
| T11 | S | B3 | Forged event posted to audit by any pod or from outside | M/H | Mitigate: NetworkPolicy on 8081, HMAC signature over raw body, port not routed; locally also Istio STRICT mTLS on 8081 and an AuthorizationPolicy that admits only the `expenses` ServiceAccount identity | `TestIngestRequiresValidSignature`; `verify-cluster.sh` (8081 blocked from other pods and namespaces; plaintext reset, other identity 403, expenses identity reaches the app) |
| T12 | T/R | B3 | Replay or redelivery of an event creates a second history entry | H/M | Mitigate: ±5 min timestamp window + `event_id` primary key; conflicting content rejected | `TestIngest`, `TestRecordDeduplicates`, `TestConcurrentRedelivery`, `TestWorkerRedeliversAfterLostResponse` |
| T13 | R | B4 | Audit history altered or deleted | L/H | Mitigate: INSERT/SELECT-only role, triggers against UPDATE/DELETE/TRUNCATE (also for the owner) | `TestHistoryIsAppendOnly` |
| T14 | I/E | B4 | SQL injection | L/H | Mitigate: constant SQL with parameters only; no dynamic identifiers; least-privilege roles | code review (`internal/storage`), gosec in CI |
| T15 | E | B4 | Compromised service reads the other service's data or runs DDL | M/H | Mitigate: one role per service, no cross-schema privilege, DDL only for migrators | `TestRuntimeRoleIsolation` (both services) |
| T16 | E | B5 | Compromised pod moves laterally (DB, other services, metadata service, internet) | M/H | Mitigate: default-deny NetworkPolicy, explicit flows, IMDS hop limit 1, egress only to public 443 in AWS | `verify-cluster.sh` (14 checks with controls); Terraform `metadata_options` |
| T17 | E | B5 | Container breakout via privileges | L/H | Mitigate: Pod Security `restricted` enforced, non-root, read-only root FS, no capabilities, seccomp, no SA token | `verify-cluster.sh` (root/privileged/hostPath rejected); `tests/manifests` |
| T18 | I | B1/B2 | Secrets or personal data in logs | M/M | Mitigate: allowlisted access-log fields (no headers, bodies, query strings); descriptions never logged | `TestWrapRequestIDAndMetrics` (token and query absent from logs) |
| T19 | I | B6 | Secrets committed to Git | M/H | Mitigate: secrets generated at deploy time, never rendered by kustomize; gitleaks on full history | `TestClusterBoundaries` (no Secret objects); `make secrets-scan` |
| T20 | T | B6 | Vulnerable or malicious dependency, action or base image | M/H | Mitigate: go.sum, govulncheck, pinned action SHAs, digest-pinned images, trivy, SBOM, Dependabot with cooldown | CI jobs `go`, `images`; `make vuln`, `make image-scan` |
| T21 | T | B6 | Image swapped between build and deploy | L/H | Mitigate: release builds once, signs keylessly, records provenance; deploy accepts digests only | `scripts/aws/deploy.sh` refuses tags; admission verification is **not** in place (R4) |
| T22 | S/E | B7 | Kubernetes API or SSH exposed | M/H | Mitigate: only 80/443 open; operator access via Session Manager tunnel | Terraform security group; `make tf-scan` |
| T23 | D | B1 | Request flood, oversized bodies, slowloris | M/L | Partially mitigate: body limits, server timeouts, resource limits; **no rate limiting** (R6) | `TestSubmitInputBoundaries` (413); `server.New` timeouts |
| T24 | D | B3 | `audit` outage loses or blocks decisions | M/M | Mitigate: transactional outbox, retries, alerting | e2e `TestAuditOutageRecovery`; `make obs-demo` |
| T25 | R | B2 | Reviewer denies a decision | L/M | Mitigate: history records reviewer subject and time, plus request ID in logs of both services | `TestDecideWritesClaimAndEventTogether`; `make trace` |

## Residual risks

Accepted for this stand; each would need work before real production.
Owner: repository owner. Formal exceptions with expiry dates are in
[security/exceptions.md](security/exceptions.md).

| ID | Risk | Why accepted here | Production answer |
|---|---|---|---|
| R1 | Single node, single AZ, same-region snapshots: an instance, zone or account compromise can take data and backups together | cost of a demo | multi-AZ RDS, cross-account immutable backups |
| R2 | AWS stand: service-to-service auth is only the shared HMAC key (rotatable, but long-lived). The local stand adds Istio mTLS with per-ServiceAccount identities (SPIFFE) on audit's 8081 | no mesh on the single-node AWS stand (memory, cost) | run the same Istio policies in AWS, or SPIFFE/mTLS without a mesh |
| R3 | In-cluster traffic to PostgreSQL is not encrypted; expenses → audit is encrypted by Istio mTLS locally only | single node; traffic stays on the host | TLS to the database, mesh mTLS in every environment |
| R4 | Image signatures are produced but not verified at admission | needs a policy controller | Kyverno / Sigstore policy-controller in enforce mode |
| R5 | Kubernetes Secrets are the source of truth (base64, encrypted at rest by k3s on AWS only) | no external secret store | Secrets Manager + External Secrets, short-lived DB credentials |
| R6 | No rate limiting or WAF | demo traffic | Traefik rate-limit middleware or AWS WAF, per-user quotas |
| R7 | Labels drive NetworkPolicy and the mesh trusts the ServiceAccount: anyone who can create pods in the namespace can label a pod `expenses` and run it under the `expenses` ServiceAccount, and then passes NetworkPolicy and the AuthorizationPolicy (including future replicas, by design). The HMAC key still stops forged events | namespace access = admin here | admission policy restricting labels and ServiceAccounts, RBAC per team |
| R8 | Logs stay on the node (kubectl logs, k3s audit log); no central, tamper-proof log store | cost | ship to a separate account with retention lock |
| R9 | No velocity limits (splitting a large expense into small claims) | out of scope for v1 | per-employee limits and anomaly detection |
| R10 | Cognito users have optional MFA and password auth for the CLI demo | demo convenience | enforce phishing-resistant MFA via SSO/passkeys |
| R11 | Tokens cannot be revoked before expiry (15 min) | standard JWT trade-off | short TTL (done), revocation for refresh tokens (enabled) |

## Where this model ends

Not analysed: the AWS account and organization setup (root user, SCPs,
CloudTrail), the operator's workstation, the GitHub account and branch
protection settings, Cognito's own security, physical and legal aspects
(GDPR retention, data-subject requests). Nothing here is a certification; the
tests show that specific properties hold for the code and configuration in
this repository, not that the system is free of vulnerabilities.
