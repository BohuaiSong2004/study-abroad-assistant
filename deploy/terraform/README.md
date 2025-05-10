# AWS deployment (Terraform)

Provisions the production shape of Waypoint. **It has been validated but not
applied**: CI runs `terraform validate`, tflint with the AWS ruleset, and
Checkov (0 failed checks; each skipped check has its reason next to the
resource as a `#checkov:skip` comment). This portfolio project is not running
on AWS, so there are no production measurements of DAX.

| Piece | Resources |
| --- | --- |
| Network | VPC across 2-3 AZs, public/private subnets, NAT gateway, DynamoDB gateway endpoint, flow logs (rejected traffic), deny-all default security group |
| Data | 3 DynamoDB tables (on-demand, PITR, SSE, TTL) — one per service, same schema as `internal/ddb/tables.go` |
| Cache | DAX cluster (TLS endpoint, encryption at rest, 3 nodes), TTLs mirroring the local cache |
| Compute | ECS Fargate services for gateway, catalog, applications, timeline; CPU target tracking |
| Discovery | Cloud Map private DNS; gRPC clients use `dns:///` + round robin |
| Edge | ALB (HTTP, or HTTPS with an ACM certificate) → gateway; AWS WAF with managed rule groups and a per-IP rate limit on `/api/auth/` |
| Security | Per-service IAM task roles limited to the service's own table + DAX; internal token and bootstrap password in Secrets Manager; read-only root filesystems |
| Email | SES over SMTP: an IAM user allowed only `ses:SendRawEmail` from `mail_from_address`, its SMTP password (derived by Terraform) in Secrets Manager, optional SES domain identity (`mail_domain`) |
| Images | ECR repositories with scan-on-push, immutable tags |

Before applying: the SES SMTP key is stored in Terraform state, so use an
encrypted remote backend; and a new SES account is in the sandbox (mail only
reaches verified addresses) until production access is requested.

```sh
cd deploy/terraform
terraform init
terraform plan -var environment=staging -var image_tag=$(git rev-parse --short HEAD)
```

Push images first (tags are immutable):

```sh
for s in gateway catalog applications timeline seed; do
  docker build --build-arg SERVICE=$s -t <account>.dkr.ecr.<region>.amazonaws.com/waypoint-staging/$s:<tag> ../..
  docker push <account>.dkr.ecr.<region>.amazonaws.com/waypoint-staging/$s:<tag>
done
```

Seed the catalog with a one-off task of the `seed` image (same environment as
the catalog service).
