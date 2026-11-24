# AWS stand

A cheap, temporary, **single-node** stand: one EC2 instance running k3s with
PostgreSQL and both services, Cognito for sign-in, Let's Encrypt for TLS.
Nothing in this document has been applied; it is the plan for doing it
yourself. Cost: [cost.md](cost.md).

## Resources (infra/terraform/aws)

| Resource | Purpose | Notes |
|---|---|---|
| VPC 10.80.0.0/16, one public subnet, internet gateway | network | no NAT gateway; the default security group denies everything |
| Security group `…-node` | the only firewall | in: 443 (API), 80 (ACME + redirect); out: 443, 80. **No 22, no 6443** |
| EC2 `t4g.small`, Ubuntu 24.04 arm64 | k3s node | IMDSv2 only, hop limit 1 (pods cannot reach IMDS), encrypted 20 GB root |
| EBS data volume, encrypted gp3 10 GB | k3s state, PostgreSQL, certificates, API audit log | separate from the instance; see [instance replacement](#instance-replacement) |
| Elastic IP | stable address and `<ip>.sslip.io` hostname | $0.005/h |
| IAM role + instance profile | Session Manager only | 5 actions, no S3/ECR/Secrets |
| DLM policy + role | daily data volume snapshots, 3 kept | snapshots survive `destroy` |
| Cognito user pool (Lite), groups `employees`, `reviewers`, public client | sign-in | admin-created users only, 14+ char passwords, optional TOTP MFA, 15 min access tokens, token revocation |

### Public ports

| Port | Why it is open | What answers |
|---|---|---|
| 443 | the API | Traefik → `/v1/audit/*` to audit:8080, `/v1/*` and `/health` to expenses:8080. Nothing else is routed |
| 80 | Let's Encrypt HTTP-01 challenge | Traefik answers the challenge, redirects everything else to 443 |

Not reachable from the internet: Kubernetes API (6443, via Session Manager
port-forward only), SSH (no key pair, port closed), PostgreSQL, audit's
internal port 8081, metrics 9090, the admin listener (loopback in the pod).

## Choices

- **Image delivery: GHCR.** `.github/workflows/release.yml` builds multi-arch
  images once, pushes to `ghcr.io/npavlov/expense-platform-{expenses,audit}`,
  signs them keylessly with cosign and attaches SBOM and build provenance.
  Public images need no pull credentials on the node, so the instance role
  needs no registry access. Deploys reference digests only.
- **TLS: Let's Encrypt through the k3s Traefik.** HTTP-01 on port 80; the ACME
  account and certificates persist on the data volume. **Domain:** none needed
  by default, the stand uses `<elastic-ip-with-dashes>.sslip.io`. With your own
  domain set `domain_name` and create an A record to the Elastic IP before the
  first request. If Let's Encrypt rate-limits the shared sslip.io domain, use
  `acme_staging = true` to test or your own domain.
- **Access: Session Manager.** No SSH keys, no open admin ports; the operator
  needs IAM permissions for `ssm:StartSession` on the instance.
- **State: local** `terraform.tfstate` in `infra/terraform/aws` (git-ignored).
  Fine for one person and a short-lived stand; it contains the Cognito pool ID
  and the instance ID, not passwords.

## Prerequisites

- An AWS account you may spend money in, and an AWS Budget alert.
- AWS CLI v2 signed in with **short-lived** credentials (`aws sso login` or
  `aws sts`-issued); no long-lived access keys.
- [Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
  for the AWS CLI.
- Docker (Terraform runs from a pinned image), kubectl, jq, curl, openssl.
- GHCR images published by the `release` workflow (needs a GitHub
  environment `release` and package write permission for the repository).

## Bring up the infrastructure and application together

From the repository root, run `make aws-up` after configuring
`infra/terraform/aws/terraform.tfvars` and signing in with the AWS CLI.
Close any manual port-forward on 16443 first. The command checks the published
ARM64 release images, plans against the existing local Terraform state, and
asks for confirmation if the plan contains changes. It then waits for SSM and
k3s, installs Istio with the k3s CNI paths, deploys PostgreSQL and both services,
and checks the public HTTPS endpoint. The AWS profile sets the istiod memory
request to 256 MiB for the single-node lab. Only expenses and audit receive
sidecars. Port 8081 on audit requires mTLS and the expenses ServiceAccount.
The Go services also verify the event HMAC. A mesh rollout can use the existing
release digests.
Keep the local Terraform state across runs. To select a newer published release,
set both `EXPENSES_IMAGE` and `AUDIT_IMAGE` to its digest references.
Demo user creation is a separate operation because it resets their passwords.

## Steps

### 1. Publish images

Run the `release` workflow (Actions → release → Run workflow, or push a tag
`v0.1.0`). Copy the two digests from the run summary.

### 2. Plan and read the plan

```bash
cd infra/terraform/aws
cp terraform.tfvars.example terraform.tfvars   # set acme_email; optionally your IP in allowed_ingress_cidrs
alias tf='docker run --rm -it -v "$PWD:/tf" -w /tf -e TF_DATA_DIR=/tf/.terraform -v "$HOME/.aws:/root/.aws:ro" -e AWS_PROFILE hashicorp/terraform:1.14.0'
tf init
tf plan -out plan.tfplan
```

Map every resource in the plan to the table above: you should see about 25
resources, all `create`, one instance, two volumes, one EIP, no NAT gateway, no
load balancer, no `0.0.0.0/0` ingress except 80/443. Exercise:
[learning-path.md](learning-path.md#9-terraform).

### 3. Apply and find the resources in the console

```bash
tf apply plan.tfplan
tf output
```

In the AWS Console (eu-central-1): EC2 → Instances (the node, IMDSv2
"required"), EC2 → Volumes (two encrypted gp3 volumes), EC2 → Security Groups
(only 80/443 in), EC2 → Elastic IPs, Systems Manager → Fleet Manager (the
node appears as managed after a few minutes), Cognito → User pools, Data
Lifecycle Manager → policies. Cloud-init takes about 3-5 minutes; its log is
`/var/log/expense-platform-bootstrap.log` (Session Manager → Start session).

### 4. Deploy and verify the publication

```bash
scripts/aws/kubeconfig.sh            # admin kubeconfig into .local/aws (mode 600)
scripts/aws/users.sh                 # alice, bob, rita, victor with random passwords
EXPENSES_IMAGE=ghcr.io/npavlov/expense-platform-expenses@sha256:… \
AUDIT_IMAGE=ghcr.io/npavlov/expense-platform-audit@sha256:… \
scripts/aws/deploy.sh                # secrets, manifests, waits, checks https://<host>/health
```

Check from outside:

```bash
host=$(cd infra/terraform/aws && tf output -raw hostname)
curl -sI http://$host/health                 # 301/308 redirect to https
curl -s  https://$host/health                # {"status":"ok"} with a valid certificate
curl -s  https://$host/v1/claims             # 401 without a token
nc -vz -w 3 $host 6443; nc -vz -w 3 $host 22; nc -vz -w 3 $host 5432   # all must fail
```

### 5. Demo and tests in AWS

```bash
scripts/aws/e2e.sh                   # the same e2e suite, with Cognito tokens
TOKEN=$(scripts/aws/token.sh alice)  # manual calls
```

Restore drill against the AWS stand (tunnel open, `KUBECONFIG=.local/aws/kubeconfig`):
`KUBE_CONTEXT=expense-platform-aws NS=expense-platform make backup restore-check`.

### Teardown

```bash
tf plan -destroy -out destroy.tfplan   # read it: everything above, nothing else
tf apply destroy.tfplan
aws ec2 describe-snapshots --owner-ids self --filters Name=tag:SnapshotOf,Values=expense-platform-data \
  --query 'Snapshots[].SnapshotId' --output text | xargs -n1 aws ec2 delete-snapshot --snapshot-id
rm -rf ../../../.local/aws            # kubeconfig and demo passwords
```

Then confirm in the console that no instance, volume, Elastic IP, snapshot or
user pool remains, and in Cost Explorer the next day that charges stop.

## Instance replacement

k3s runs with `--data-dir` and local-path storage on the data volume and a
fixed node name. If the instance is replaced (`tf apply -replace=aws_instance.node`,
a new AMI or a failed host), cloud-init finds the existing filesystem, does not
format it, mounts it and starts k3s, which resumes the same cluster: same
datastore, encrypted secrets, PostgreSQL volume and certificates. The root
volume (OS, nothing else) is discarded. If the data volume itself is lost,
restore a DLM snapshot as a new volume (`aws ec2 create-volume --snapshot-id …`),
import it into the Terraform state in place of `aws_ebs_volume.data`, and apply.

## Verified locally vs. needs AWS

| Property | Local verification | Needs AWS |
|---|---|---|
| Terraform syntax, types, provider schema | `make tf-fmt tf-validate tf-lint` | — |
| Misconfigurations (open ports, encryption, IMDS) | `make tf-scan` (one documented exception) | — |
| Kubernetes manifests (schemas, PSS restricted, no exposure, no dev shortcuts) | `make k8s-lint` | — |
| Same token verifier for Cognito-shaped tokens | unit tests with Cognito claim layout | real Cognito tokens (`scripts/aws/e2e.sh`) |
| App behaviour, outbox recovery, NetworkPolicy | full suite on Docker Desktop (kindnet enforces policies) | k3s's embedded policy controller: run `verify-cluster` there |
| cloud-init, k3s install, data volume mount, Traefik ACME and redirect | — | steps 3-4 |
| IAM role sufficient for Session Manager | — | step 3 (node appears in Fleet Manager) |
| IMDS unreachable from pods | `verify-cluster` checks 169.254.169.254 is blocked by policy | hop limit 1 on the real instance |
| Instance replacement keeps state | — | `tf apply -replace=aws_instance.node`, then e2e |
| DLM snapshots created | — | after 03:00 UTC |
| Cost | estimate in [cost.md](cost.md) | Cost Explorer |
