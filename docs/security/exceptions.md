# Exception register

Every deviation from a scanner rule or from the security baseline is listed
here with its risk, compensating controls, owner and expiry. An expired entry
must be fixed or re-approved. Owner for all entries: repository owner.

| ID | What | Risk | Compensating controls | Expires |
|---|---|---|---|---|
| EX-1 | gosec G304/G703 in `libs/platform/server.EnvSecret`: file path from an environment variable | reading an arbitrary file | the path is deployment configuration written by the operator in the manifest, never request data; read-only root FS; non-root | 2027-05-31 |
| EX-2 | gosec G404 in `outbox.Backoff`: `math/rand` | predictable jitter | value only spreads retries; no security decision depends on it | 2027-05-31 |
| EX-3 | trivy AVD-AWS-0104: node egress 443/80 to 0.0.0.0/0 (`.trivyignore.yaml`) | exfiltration from a compromised node | no other ports; pods limited to public 443 by NetworkPolicy; IMDS hop limit 1; the instance role can only register with SSM. VPC endpoints would cost more than the stand | 2027-05-31 |
| EX-4 | trivy AVD-KSV-0109: "secrets" in the `db-bootstrap-sql` ConfigMap | false positive | the SQL contains psql placeholders only; values come from a Secret at run time | 2027-05-31 |
| EX-5 | Migrator roles hold `CREATE ON DATABASE` | a migrator could create another schema | needed because migration 001 (already applied) runs `CREATE SCHEMA IF NOT EXISTS`; migrator credentials exist only in migration Jobs; runtime roles have no such privilege | until a squashed baseline migration replaces 001 |
| EX-6 | `sslmode=disable` between pods and PostgreSQL | sniffing on the node network | single node; NetworkPolicy limits who can connect; SCRAM authentication | production: R3 |
| EX-7 | Shared HMAC key for signing events | long-lived shared secret | key IDs and dual-key rotation, timestamp window, NetworkPolicy, event dedup; Istio mTLS + identity-based AuthorizationPolicy | production: R2 |
