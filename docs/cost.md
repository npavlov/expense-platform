# Cost of the AWS stand

**Region:** eu-central-1 (Frankfurt). **Prices:** on-demand list prices in USD,
read on **2026-11-20** from AWS's public price files (EC2 and EBS, published
2026-11-20) and the AWS public IPv4 announcement. Taxes (VAT) are not included.
Account credits or free tiers are **not** counted: they do not guarantee a
zero bill, and free-tier eligibility depends on the account.

## Assumptions

- Defaults of `infra/terraform/aws`: one `t4g.small`, 20 GB gp3 root, 10 GB gp3
  data volume, one Elastic IP, daily snapshots with 3 kept.
- About 2 GB on the data volume (k3s state and container images, PostgreSQL,
  certificates); snapshots are incremental.
- Demo traffic only: far below 100 GB/month of data transfer out.
- Images on GHCR (public, free); hostname `<ip>.sslip.io` (no Route 53).
- No CloudWatch Logs, detailed monitoring, NAT gateway, load balancer, EKS.

## Unit prices

| Item | Price |
|---|---|
| EC2 t4g.small (Linux) | $0.0192 / hour |
| EC2 t4g.medium (Linux), if more headroom is needed | $0.0384 / hour |
| Public IPv4 address (the Elastic IP, in use) | $0.005 / hour |
| EBS gp3 storage (3,000 IOPS and 125 MB/s included) | $0.0952 / GB-month |
| EBS snapshot storage | $0.054 / GB-month |
| Cognito, Lite tier | first 10,000 monthly active users free; 4 demo users |
| Session Manager, Data Lifecycle Manager | no charge (snapshots billed above) |
| Data transfer out | first 100 GB/month free across the account |
| GHCR public images | free |

## Estimate

| Item | 8 hours (one demo day) | Left running for a month (730 h) |
|---|---|---|
| EC2 t4g.small | $0.15 | $14.02 |
| Public IPv4 | $0.04 | $3.65 |
| EBS gp3, 30 GB (billed per second while the volumes exist) | $0.03 | $2.86 |
| Snapshots (~2 GB incremental, 3 kept) | $0.00 (≤ 1 snapshot) | ~$0.12 |
| Cognito, transfer, GHCR, SSM | $0.00 | $0.00 |
| **Total** | **≈ $0.23** | **≈ $20.65** |

With `instance_type = "t4g.medium"` the month is about $34.67.

## What keeps costing after `terraform destroy`

- **Snapshots created by DLM are not deleted by Terraform.** List and delete them:
  `aws ec2 describe-snapshots --owner-ids self --filters Name=tag:SnapshotOf,Values=expense-platform-data`
  then `aws ec2 delete-snapshot --snapshot-id …` (see [aws.md](aws.md#teardown)).
- GHCR images are free but stay public until deleted in GitHub.

## Guard rails

Create an AWS Budget (for example $10/month with an e-mail at 50%) before the
first `apply`; Budgets are free for the first two. Check Cost Explorer the day
after a demo: EC2 and EBS appear within about 24 hours.

Sources: AWS price files (`b0.p.awsstatic.com/pricing/2.0/meteredUnitMaps/ec2/USD/current/…`),
[AWS public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/),
[Amazon EC2 on-demand pricing](https://aws.amazon.com/ec2/pricing/on-demand/),
[Amazon EBS pricing](https://aws.amazon.com/ebs/pricing/). Re-check them before
applying; prices change.
