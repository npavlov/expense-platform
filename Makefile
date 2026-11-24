# Run `make help` for the list of targets.
#
# Requirements: Go 1.26+, Docker Desktop with Kubernetes enabled, kubectl, jq,
# curl, openssl; istioctl at the version in scripts/istio-version.sh (or run
# `make istioctl`). Scanners and Terraform run from pinned container images or
# pinned `go run` versions, so nothing else has to be installed.

MODULES := libs/platform services/expenses services/audit tools/devidp tests

GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.10.0
GOSEC       := github.com/securego/gosec/v2/cmd/gosec@v2.30.0
KUBECONFORM := github.com/yannh/kubeconform/cmd/kubeconform@v0.8.0
TERRAFORM   := hashicorp/terraform:1.14.0@sha256:3abcdb56739bf9c61a0290cfd1a2e41ef9c3799c0e6fa7f3c467f883367d3ecb
TFLINT      := ghcr.io/terraform-linters/tflint:v0.60.0@sha256:cef181224b4a9cea521d8f785d50957ea3215b449e2d97e7793f222e2808d188
TRIVY       := aquasec/trivy:0.67.2@sha256:e2b22eac59c02003d8749f5b8d9bd073b62e30fefaef5b7c8371204e0a4b0c08
GITLEAKS    := zricethezav/gitleaks:v8.29.0@sha256:71d3ee5990f2176f763b438298453fc37e87b119122045e176ca9d44ff00b08b

REDOCLY     := redocly/cli:2.11.1@sha256:681bd61aa0c5a0bdc8860d482746dc02747118ef7a0488710cfae87f1b4848ca
ACTIONLINT  := rhysd/actionlint:1.7.8@sha256:96d4a8c87dbbfb3bdd324f8fdc285fc3df5261e2decc619a4dd7e8ee52bbfd46

TF_DIR := infra/terraform/aws

.DEFAULT_GOAL := help

.PHONY: aws-up help istioctl test vet fmt fmt-check test-integration fuzz vuln sast k8s-lint \
        local-up local-status local-stop local-start local-destroy clean-images port-forward port-forward-stop \
        e2e demo obs-demo verify-cluster backup restore-check outbox-status trace \
        tf-fmt tf-validate tf-lint tf-scan api-lint gha-lint secrets-scan image-scan check

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[1m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## --- Code ---------------------------------------------------------------

test: ## Unit tests (race detector) for every module
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go test -race -count=1 ./...) || exit 1; done

vet: ## go vet, including integration and e2e build tags
	@for m in $(MODULES); do (cd $$m && go vet ./... && go vet -tags integration,e2e ./...) || exit 1; done

fmt: ## Format Go code
	gofmt -w $(MODULES)

fmt-check: ## Fail if Go code is not formatted
	@out="$$(gofmt -l $(MODULES))"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

test-integration: ## Integration tests against a throwaway PostgreSQL (Docker)
	./scripts/test-integration.sh

fuzz: ## Short fuzzing run of the parsers (FUZZTIME=30s)
	cd libs/platform && go test -run=^$$ -fuzz=FuzzVerify -fuzztime=$${FUZZTIME:-15s} ./sigauth
	cd libs/platform && go test -run=^$$ -fuzz=FuzzDecodeJSON -fuzztime=$${FUZZTIME:-15s} ./httpx
	cd services/audit && go test -run=^$$ -fuzz=FuzzParse -fuzztime=$${FUZZTIME:-15s} ./internal/events

vuln: ## Known vulnerabilities in reachable dependency code (govulncheck)
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go run $(GOVULNCHECK) ./...) || exit 1; done

sast: ## Static analysis (gosec)
	@for m in libs/platform services/expenses services/audit tools/devidp; do echo "== $$m"; (cd $$m && go run $(GOSEC) -quiet -exclude-generated ./...) || exit 1; done

k8s-lint: ## Validate rendered overlays against Kubernetes schemas and the policy tests
	@for o in local local-observability aws; do echo "== $$o"; kubectl kustomize deploy/k8s/overlays/$$o | go run $(KUBECONFORM) -strict -summary -kubernetes-version 1.33.0 - || exit 1; done
	cd tests && go test -count=1 ./manifests/

## --- Local stand (Docker Desktop Kubernetes, namespace expense-platform-local)

istioctl: ## Install the pinned, checksum-verified istioctl into .local/bin
	./scripts/install-istioctl.sh

aws-up: ## Provision AWS and deploy the pinned release through HTTPS readiness
	bash scripts/aws/up.sh

local-up: ## Build images and deploy or update the stand (OBSERVABILITY=1 adds Prometheus)
	./scripts/deploy-local.sh

local-status: ## Pods, jobs, volumes and endpoint health
	./scripts/status.sh

local-stop: ## Stop all workloads, keep data and secrets
	./scripts/port-forward.sh stop
	kubectl --context docker-desktop -n expense-platform-local scale deployment --all --replicas=0
	kubectl --context docker-desktop -n expense-platform-local scale statefulset/postgres --replicas=0

local-start: ## Start a stopped stand again (no rebuild)
	kubectl --context docker-desktop -n expense-platform-local scale statefulset/postgres --replicas=1
	kubectl --context docker-desktop -n expense-platform-local rollout status statefulset/postgres --timeout=180s
	kubectl --context docker-desktop -n expense-platform-local scale deployment --all --replicas=1
	./scripts/port-forward.sh start
	./scripts/status.sh

local-destroy: ## DELETE the stand with its database (requires CONFIRM=delete-local-data)
	./scripts/port-forward.sh stop
	./scripts/destroy-local.sh

clean-images: ## Remove all locally built expense-platform images
	@docker images 'expense-platform/*' -q | sort -u | xargs docker rmi -f 2>/dev/null || true

port-forward: ## Forward the APIs to 127.0.0.1:30080-30082 (+30090 Prometheus)
	./scripts/port-forward.sh start

port-forward-stop: ## Stop the port-forwards
	./scripts/port-forward.sh stop

e2e: ## End-to-end tests through the published local APIs (stops/starts audit)
	cd tests && E2E_DISRUPTIVE=1 go test -tags e2e -count=1 -v ./e2e/

demo: ## Five-minute scripted demo
	./scripts/demo.sh

obs-demo: ## Graph and OutboxDeliveryStalled alert while audit is down (needs OBSERVABILITY=1)
	./scripts/obs-demo.sh

verify-cluster: ## Pod Security Admission and NetworkPolicy enforcement checks
	./scripts/verify-cluster.sh

backup: ## pg_dump of the stand into backups/
	./scripts/backup.sh

restore-check: ## Restore the newest dump into a clean PostgreSQL and verify it
	./scripts/restore-check.sh

outbox-status: ## Undelivered outbox events (admin endpoint via port-forward)
	./scripts/outbox.sh status

trace: ## Follow a request through both services: make trace REQUEST_ID=...
	./scripts/trace.sh $(REQUEST_ID)

## --- Infrastructure and supply chain (offline) ---------------------------

tf-fmt: ## terraform fmt -check
	docker run --rm -v $(CURDIR)/$(TF_DIR):/tf -w /tf $(TERRAFORM) fmt -check -recursive -diff

tf-validate: ## terraform init (no backend) and validate; refreshes the provider lock for all CI/dev platforms
	docker run --rm -v $(CURDIR)/$(TF_DIR):/tf -w /tf -e TF_DATA_DIR=/tf/.terraform --entrypoint sh $(TERRAFORM) -c '\
	  terraform init -backend=false -input=false >/dev/null && \
	  terraform providers lock -platform=linux_amd64 -platform=linux_arm64 -platform=darwin_arm64 >/dev/null && \
	  terraform validate'

tf-lint: ## tflint with the AWS ruleset (GITHUB_TOKEN, if set, avoids GitHub API rate limits on plugin download)
	docker run --rm -e GITHUB_TOKEN -v $(CURDIR)/$(TF_DIR):/data -w /data --entrypoint sh $(TFLINT) -c 'tflint --init >/dev/null && tflint --format compact'

tf-scan: ## Misconfiguration scan of Terraform and the rendered Kubernetes overlays (trivy)
	@mkdir -p .local/scan/k8s
	@for o in local local-observability aws; do kubectl kustomize deploy/k8s/overlays/$$o > .local/scan/k8s/$$o.yaml; done
	@for d in $(TF_DIR) .local/scan/k8s; do echo "== $$d"; \
	  docker run --rm -v $(CURDIR):/src -w /src $(TRIVY) config --quiet --disable-telemetry --exit-code 1 \
	    --severity HIGH,CRITICAL --ignorefile .trivyignore.yaml $$d || exit 1; done

api-lint: ## Lint the OpenAPI contracts (Redocly)
	docker run --rm -v $(CURDIR)/contracts:/c -w /c/http $(REDOCLY) lint expenses.openapi.yaml audit.openapi.yaml

gha-lint: ## Lint GitHub Actions workflows (actionlint)
	docker run --rm -v $(CURDIR):/repo -w /repo $(ACTIONLINT) -color

secrets-scan: ## Secrets in the working tree and the whole Git history (gitleaks)
	docker run --rm -v $(CURDIR):/repo -w /repo $(GITLEAKS) git --redact --no-banner /repo

image-scan: ## Build images and scan them for fixable HIGH/CRITICAL vulnerabilities
	@for s in services/expenses:expenses services/audit:audit tools/devidp:devidp; do \
	  d=$${s%%:*}; n=$${s##*:}; docker build -q -f $$d/Dockerfile -t expense-platform/$$n:scan . >/dev/null && \
	  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock $(TRIVY) image --quiet --exit-code 1 \
	    --severity HIGH,CRITICAL --ignore-unfixed expense-platform/$$n:scan || exit 1; done

check: fmt-check vet test test-integration k8s-lint vuln sast secrets-scan api-lint gha-lint tf-fmt tf-validate tf-lint tf-scan ## Everything CI runs, locally
