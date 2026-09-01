# dufflebag

SWAGGER_VERSION := v0.35.3
SWAGGER         := $(shell go env GOPATH)/bin/swagger
GOLANGCI_VERSION := v2.11.3
GOLANGCI        := $(shell go env GOPATH)/bin/golangci-lint
SQLC_VERSION    := v1.30.0
OAPI_VERSION    := v2.8.0
OAPI            := $(shell go env GOPATH)/bin/oapi-codegen
SQLC            := $(shell go env GOPATH)/bin/sqlc
GO_LICENSES_VERSION := v1.6.0
GO_LICENSES     := $(shell go env GOPATH)/bin/go-licenses
CYCLONEDX_VERSION := v1.11.0
CYCLONEDX       := $(shell go env GOPATH)/bin/cyclonedx-gomod
SPECS           := spec/vendor
HCP2023_SPEC_OVERLAY := spec/overlays/hcp2023-version-revoke-at.py
PREVIOUS_REF    ?= HEAD
SCHEMA_COMPAT   ?= $(CURDIR)/.schema-compat
PACKER_E2E_PACKER    ?= packer
PACKER_E2E_EXPECT_VERSION ?=
PACKER_E2E_DOCKER    ?= docker
PACKER_E2E_HOSTNAME  ?= dufflebag.example.com
PACKER_E2E_CERT_FILE ?= $(HOME)/.config/dufflebag/tls.crt
PACKER_E2E_KEY_FILE  ?= $(HOME)/.config/dufflebag/tls.key
# Resolved against the MAIN checkout's parent, not CURDIR's: from a linked
# git worktree, $(CURDIR)/.. is the worktrees parent rather than the repo's
# neighbour, so the lane fails its precondition on a path no lab setup ever
# wrote (duf-svvf). --git-common-dir names the main repository's .git
# wherever the invocation runs.
PACKER_E2E_CA_FILE   ?= $(abspath $(shell git rev-parse --path-format=absolute --git-common-dir)/../../ca-chain.pem)
PACKER_E2E_IMAGE     ?= alpine:3.20
CLOUD_JUNIT          ?=
CLOUD_SHOTS          ?= 0
CLOUD_SHOTS_DIR      ?= $(CURDIR)/shots
CLOUD_CHROME         ?=
SCANNER_DOCKER       ?= docker
SCANNER_TEST_IMAGE   ?= dufflebag-scanner-test:dev
OSV_STUB_IMAGE       ?= dufflebag-osv-stub:dev
SCANNER_DISABLED_ENV := env -u DFBG_SCANNER_ADAPTER -u DFBG_SCANNER_ENDPOINT \
	-u DFBG_SCANNER_FORMAT -u DFBG_SCANNER_REQUEST_TIMEOUT -u DFBG_SCANNER_PASS_TIMEOUT \
	-u DFBG_SCANNER_RUN_RETENTION -u DFBG_SCANNER_WORKERS -u DFBG_SCANNER_CA_FILE \
	-u DFBG_SCANNER_INTERVAL

# The demo stack is a LONG-LIVED instance for looking at the console, unlike
# test-packer which stands one up and tears it down inside a single gate.
DEMO_DIR       ?= $(CURDIR)/.demo
DEMO_PORT      ?= 8443
DEMO_CONTAINER ?= dufflebag-demo
# dufflebag runs as a container alongside its backing services, on a shared
# network where they reach each other by service name — the way a real
# deployment is wired, not a host process reaching published ports.
DEMO_SERVER_CONTAINER ?= dufflebag-demo-server
DEMO_NET       ?= dufflebag-demo-net
# SBOMs live in object storage, so a demo without one refuses every upload.
DEMO_S3_CONTAINER ?= dufflebag-demo-s3
DEMO_S3_IMAGE     ?= quay.io/benjamin_holmes/ceph-aio:v20
DEMO_S3_BUCKET    ?= dufflebag-demo
DEMO_VAULT_CONTAINER ?= dufflebag-demo-vault
DEMO_VAULT_IMAGE     ?= hashicorp/vault:2.0.3
DEMO_VAULT_TOKEN     ?= demo-root
# DEMO_CLAIM=0 stops after the stack serves, leaving the instance unclaimed
# for a first-run wizard walkthrough. Never expose an unclaimed instance.
DEMO_CLAIM           ?= 1
# demo-publish takes its tenancy from the SDK's own environment
# (HCP_ORGANIZATION_ID/HCP_PROJECT_ID — the Instance page's connection block
# exports exactly these), falling back to the files make demo-up writes.
# First run is SPN, then organisation, then project. /sys/init mints only the
# principal; tenancy is created through the ordinary authenticated endpoints,
# which is what the console wizard does and what this target emulates. Claiming
# an instance without them leaves it signed-in and unusable.
# The demo publishes one bucket per corpus distro, each pinned to a
# DELIBERATELY OLD image. A current image would very likely show nothing, and a
# console that says "no known findings" teaches the reader nothing about what
# the scanner does — the point of the demo is to see real advisories against
# real package inventories. alpine 3.17 and debian 11 are past their support
# windows, ubi8 exercises the Red Hat two-phase stream confirmation on el8
# (the major our spike controls used), and ubuntu 20.04 is the one ecosystem
# the spike never scanned, so this is also its first real exercise.
#
# Format: bucket=image, space separated.
DEMO_DISTROS ?= demo-alpine=alpine:3.17 demo-debian=debian:11 \
	demo-ubi=registry.access.redhat.com/ubi8/ubi:latest demo-ubuntu=ubuntu:20.04

DEMO_ORG     ?= demo-organisation
DEMO_PROJECT ?= demo-project
DEMO_NGROK     ?= ngrok
DEMO_NGROK_URL ?=

IMAGE         ?= quay.io/benjamin_holmes/dufflebag
IMAGE_TAG     ?= dev
VERSION       ?= dev
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# RC images expire; release images do not (conventions rule 6). The push
# targets encode that split so CI cannot get it wrong per-invocation.
IMAGE_EXPIRES ?= 2w

# The image demo-up runs: the continuous head-of-main build that ci.yml
# publishes to quay.io/.../dufflebag:latest on every green push. Override
# DEMO_IMAGE_TAG to demo a specific release or RC, or DEMO_IMAGE to point at
# another registry entirely.
DEMO_IMAGE_TAG ?= latest
DEMO_IMAGE     ?= $(IMAGE):$(DEMO_IMAGE_TAG)

.PHONY: help
help:
	@grep -hE '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

$(SWAGGER):
	go install github.com/go-swagger/go-swagger/cmd/swagger@$(SWAGGER_VERSION)

$(GOLANGCI):
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

$(SQLC):
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

$(OAPI):
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_VERSION)

$(GO_LICENSES):
	go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)

$(CYCLONEDX):
	go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_VERSION)

.PHONY: generate-platform
generate-platform: $(OAPI) ## Regenerate the platform plane server interface from its spec
	$(OAPI) --config internal/platform/v1/oapi-codegen.yaml spec/platform/openapi.yaml

.PHONY: generate-sql
generate-sql: $(SQLC) ## Regenerate the Postgres query package
	$(SQLC) generate

.PHONY: generate
generate: $(SWAGGER) ## Regenerate compat models from the vendored specs
	@set -e; work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; \
		cp $(SPECS)/cloud-packer-service/2023-01-01/hcp.swagger.json "$$work/hcp.swagger.json"; \
		python3 $(HCP2023_SPEC_OVERLAY) "$$work/hcp.swagger.json"; \
		rm -rf internal/compat/hcp2023/models; \
		$(SWAGGER) generate model --spec="$$work/hcp.swagger.json" \
			--target=internal/compat/hcp2023 --model-package=models --quiet
# --skip-validation: HashiCorp's published resource-manager spec fails Swagger 2.0
# validation on ambiguous paths we do not serve. Models do not depend on paths.
	@rm -rf internal/compat/rm2019/models
	$(SWAGGER) generate model --spec=$(SPECS)/cloud-resource-manager/2019-12-10/hcp.swagger.json \
		--target=internal/compat/rm2019 --model-package=models --skip-validation --quiet
	go mod tidy

.PHONY: generate-check
# Covers BOTH generators. sqlc output was previously outside this check, which
# let a hand-written postgresdb package pass build, test, lint and this target
# while differing from what sqlc actually produces. Compare the generated diff
# before and after rather than comparing with HEAD: review trees are deliberately
# uncommitted, and a correct generated change must still be checkable there.
generate-check: ## Fail if generated code is stale relative to its inputs
	@set -e; before=$$(mktemp); after=$$(mktemp); trap 'rm -f "$$before" "$$after"' EXIT; \
		git diff --binary -- internal/compat/hcp2023/models internal/compat/rm2019/models internal/store/postgres/postgresdb internal/platform/v1/api.gen.go > "$$before"; \
		$(MAKE) generate generate-sql generate-platform; \
		git diff --binary -- internal/compat/hcp2023/models internal/compat/rm2019/models internal/store/postgres/postgresdb internal/platform/v1/api.gen.go > "$$after"; \
		if ! cmp -s "$$before" "$$after"; then \
			echo "generated code is stale — run 'make generate generate-sql generate-platform'"; \
			diff -u "$$before" "$$after" || true; \
			exit 1; \
		fi

.PHONY: build-ui
build-ui: ## Build the web console when npm is available
	@if command -v npm >/dev/null 2>&1; then \
		cd web && { [ -d node_modules ] || npm ci; } && npm run typecheck && npm run build; \
	else \
		echo "npm not found; building with the checked-in placeholder web console"; \
	fi

.PHONY: docs
# CHART_VERSION is stamped by CI (0.1.<run number>) so helm clients see
# upgrades; local builds carry an obvious development version.
CHART_VERSION ?= 0.0.0-dev
DOCS_SITE_URL ?= https://benemon.github.io/dufflebag

docs: ## Build the documentation site, platform API reference and Helm repo
	mkdir -p docs-site/public/charts
	./docs-site/node_modules/.bin/redocly build-docs spec/platform/openapi.yaml --output docs-site/public/platform-api.html
	helm package deploy/helm/dufflebag --version "$(CHART_VERSION)" --destination docs-site/public/charts >/dev/null
	helm repo index docs-site/public/charts --url "$(DOCS_SITE_URL)/charts"
	./docs-site/node_modules/.bin/vitepress build docs-site

.PHONY: test-ui
test-ui: ## Test user-visible web console states
	@if command -v npm >/dev/null 2>&1; then \
		cd web && { [ -d node_modules ] || npm ci; } && npm test; \
	else \
		echo "npm not found; skipping web console tests"; \
	fi

.PHONY: build
build: build-ui ## Build the web console and all Go packages
	go build ./...

SERVER_BIN ?= $(CURDIR)/dufflebag

.PHONY: server
server: build-ui ## Build the server binary, console embedded, at SERVER_BIN
	go build -o $(SERVER_BIN) ./cmd/dufflebag

# Every gate below stands up a server. Left to themselves they each compile one,
# which in CI means the same commit is built four times and no lane proves the
# artifact another lane tested. Point DUFFLEBAG_BIN at a binary built once — the
# output of `make server` — and they all drive that exact one instead.
DUFFLEBAG_BIN ?=

# Resolves the binary a gate drives: the shared one when it was handed over,
# otherwise a fresh build inside the gate's own work directory, so every target
# still runs standalone with no arguments.
define resolve-server-bin
	if [ -n "$(DUFFLEBAG_BIN)" ]; then \
		test -x "$(DUFFLEBAG_BIN)" || { echo "DUFFLEBAG_BIN is not executable: $(DUFFLEBAG_BIN)"; exit 1; }; \
		bin="$(DUFFLEBAG_BIN)"; \
		echo "driving the prebuilt server: $$bin"; \
	else \
		go build -o "$$work/dufflebag" ./cmd/dufflebag; \
		bin="$$work/dufflebag"; \
	fi
endef

.PHONY: test-build-clean
test-build-clean: ## Prove a real build adds no working-tree changes
	@set -e; before=$$(mktemp); after=$$(mktemp); trap 'rm -f "$$before" "$$after"' EXIT; \
		git status --porcelain > "$$before"; \
		$(MAKE) build; \
		git status --porcelain > "$$after"; \
		if ! cmp -s "$$before" "$$after"; then \
			echo "FAIL: make build changed the working tree"; \
			diff -u "$$before" "$$after" || true; \
			exit 1; \
		fi

.PHONY: test
test: test-ui ## Run tests
	go test ./...

.PHONY: test-live-conformance
# Live-account access required. In CI this runs only as the manual
# workflow_dispatch lane (live-conformance.yml), never on pull requests;
# credentials come from the environment there and from the env file locally.
test-live-conformance: ## Check the public compatibility dossier against live HCP
	@set -a; \
	if [ -z "$${HCP_CLIENT_ID:-}" ] || [ -z "$${HCP_CLIENT_SECRET:-}" ]; then \
	env_file="$${HCP_SPN_ENV_FILE:-hcp-packer-spn.env}"; \
	if [ ! -f "$$env_file" ]; then \
		echo "live HCP conformance credentials not found: $$env_file" >&2; \
		echo "set HCP_SPN_ENV_FILE to a file exporting HCP_CLIENT_ID/HCP_CLIENT_SECRET" >&2; \
		exit 1; \
	fi; \
	. "$$env_file"; \
	fi; \
	set +a; \
	go test -tags=liveconf ./e2e/liveconf/ -count=1 -timeout 15m

.PHONY: test-without-scanner
# CI runs the scanner package on an internal Docker network first, so running
# it again on the internet-routed hosted runner would weaken the egress proof.
test-without-scanner: test-ui ## Run unit tests outside the isolated scanner lane
	go test $$(go list ./... | awk '$$0 != "github.com/benemon/dufflebag/internal/scan"')

.PHONY: test-scanner
# The internal network makes a mistaken api.osv.dev call fail at connect time;
# the separate stub container also proves the recorded server is reusable.
test-scanner: ## Run scanner tests against recorded fixtures with no external route
	@set -e; \
	buildx=$$(mktemp -d); network=dufflebag-scanner-$$$$; stub=; test_container=; \
	trap 'test -z "$$test_container" || $(SCANNER_DOCKER) rm -f "$$test_container" >/dev/null 2>&1 || true; test -z "$$stub" || $(SCANNER_DOCKER) rm -f "$$stub" >/dev/null 2>&1 || true; $(SCANNER_DOCKER) network rm "$$network" >/dev/null 2>&1 || true; rm -rf "$$buildx"' EXIT; \
	BUILDX_CONFIG="$$buildx" $(SCANNER_DOCKER) build -f Containerfile.scanner --target test -t $(SCANNER_TEST_IMAGE) .; \
	BUILDX_CONFIG="$$buildx" $(SCANNER_DOCKER) build -f Containerfile.scanner --target stub -t $(OSV_STUB_IMAGE) .; \
	$(SCANNER_DOCKER) network create --internal "$$network" >/dev/null; \
	internal=$$($(SCANNER_DOCKER) network inspect -f '{{.Internal}}' "$$network"); \
	[ "$$internal" = true ] || { echo "FAIL: scanner network has an external route"; exit 1; }; \
	stub=$$($(SCANNER_DOCKER) run -d --network "$$network" --network-alias osv-stub $(OSV_STUB_IMAGE)); \
	test_container=$$($(SCANNER_DOCKER) create --network "$$network" \
		-e OSV_STUB_ENDPOINT=http://osv-stub:8080 $(SCANNER_TEST_IMAGE) -test.count=1); \
	$(SCANNER_DOCKER) start -a "$$test_container"

.PHONY: test-integration
# These tests exercise scanner persistence with an in-memory adapter. The live
# provider contract belongs to test-scanner, where Docker blocks egress.
# The default 10m timeout is not enough: each suite starts its own Postgres
# and Ceph containers, and a CI runner is slower than a laptop at both.
test-integration: ## Run Postgres and Vault integration tests in testcontainers
	$(SCANNER_DISABLED_ENV) go test -timeout 30m -tags=integration ./internal/store/postgres ./internal/keyring ./cmd/dufflebag

.PHONY: test-migration-equivalence
test-migration-equivalence: ## Prove the 0.1.0 baseline matches the pre-release chain
	./internal/store/postgres/check-baseline-equivalence.sh

.PHONY: test-rls-sabotage
# Prove the tenant isolation assertions have teeth, by requiring them to FAIL
# when row-level security is sabotaged. A test that passes with RLS disabled is
# asserting nothing, and would never say so.
#
# The exit code alone is not enough: `go test` also exits non-zero for a build
# or setup failure, so a broken test file or an unreachable Docker daemon would
# read as "sabotage detected" and turn this gate green while proving nothing.
# The output must show TestTenantIsolation actually ran and actually failed.
test-rls-sabotage: ## Prove tenant isolation tests fail under RLS sabotage
	@set -e; \
	for hook in DUFFLEBAG_TEST_DROP_RLS DUFFLEBAG_TEST_DISABLE_RLS; do \
		if out=$$(env $$hook=1 go test -tags=integration ./internal/store/postgres \
			-run '^TestTenantIsolation$$$$' -count=1 2>&1); then \
			echo "$$out"; \
			echo "FAIL: tenant isolation PASSED under $$hook — the assertions prove nothing"; \
			exit 1; \
		fi; \
		if ! echo "$$out" | grep -q -- '--- FAIL: TestTenantIsolation'; then \
			echo "$$out"; \
			echo "FAIL: $$hook did not run TestTenantIsolation to a failure (build or setup error), so nothing was proved"; \
			exit 1; \
		fi; \
		echo "$$hook: tenant isolation failed as required"; \
	done; \
	for table in versions channels builds artifacts channel_assignments \
		sboms sbom_packages scan_runs scan_findings scan_transcripts \
		build_scan_state pending_scans pins; do \
		if out=$$(env DUFFLEBAG_TEST_DROP_BUCKET_POLICY=$$table go test -tags=integration ./internal/store/postgres \
			-run '^TestTenantIsolation$$$$' -count=1 2>&1); then \
			echo "$$out"; \
			echo "FAIL: tenant isolation PASSED with $$table's bucket predicate dropped — the assertions prove nothing"; \
			exit 1; \
		fi; \
		if ! echo "$$out" | grep -q -- '--- FAIL: TestTenantIsolation'; then \
			echo "$$out"; \
			echo "FAIL: dropping $$table's bucket predicate did not run TestTenantIsolation to a failure (build or setup error), so nothing was proved"; \
			exit 1; \
		fi; \
		echo "$$table: bucket isolation failed as required"; \
	done

.PHONY: test-contract
test-contract: ## Drive a running server with hcp-sdk-go's generated client
	cd contract && go test ./...

.PHONY: test-e2e-terraform
# Deliberately outside the default test target: this gate drives the real
# Terraform CLI and provider, so it catches client behaviour below either
# side's contract tests (ADR-0013; duf-kzo). Like test-smoke, the test owns a
# random-port Postgres container and freshly built server, then verifies its
# teardown.
test-e2e-terraform: ## Drive real Terraform and terraform-provider-hcp against a live stack
	@set -e; work=$$(mktemp -d "$(CURDIR)/e2e/terraform/.work.XXXXXX"); \
	trap 'rm -rf "$$work"' EXIT; \
	cp e2e/terraform/main.tf e2e/terraform/.terraform.lock.hcl "$$work"; \
	$(resolve-server-bin); \
	$(SCANNER_DISABLED_ENV) E2E_TERRAFORM_BIN="$$bin" E2E_TERRAFORM_WORK="$$work" \
		node --test e2e/terraform/e2e.test.mjs

# The encrypted posture of the gate above (duf-egk2.7): the provider's data
# sources and channel-assignment writes against MAC-verified rows (ADR-0024).
# Same Vault-dev-container shape as test-packer-ci-encrypted; unlike the packer
# gate it needs no in-run CA — the harness self-signs and pins trust off — so
# it runs locally on macOS too. CI runs it BESIDE the unencrypted gate, not
# instead of it: unencrypted is what most deployments run, and it is the
# posture every other lane in the job drives.
TERRAFORM_E2E_VAULT ?= dufflebag-terraform-vault

.PHONY: test-e2e-terraform-encrypted
test-e2e-terraform-encrypted: ## Run the Terraform gate encrypted against a Vault dev container
	@set -e; \
	trap 'docker rm -f $(TERRAFORM_E2E_VAULT) >/dev/null 2>&1 || true' EXIT; \
	token=$$(openssl rand -hex 16); \
	docker rm -f $(TERRAFORM_E2E_VAULT) >/dev/null 2>&1 || true; \
	docker run -d --name $(TERRAFORM_E2E_VAULT) \
		-e VAULT_DEV_ROOT_TOKEN_ID="$$token" -p 127.0.0.1::8200 hashicorp/vault:1.17 >/dev/null; \
	port=$$(docker port $(TERRAFORM_E2E_VAULT) 8200/tcp | head -1 | sed 's/.*://'); \
	addr="http://127.0.0.1:$$port"; \
	for _ in $$(seq 1 60); do \
		curl -sf "$$addr/v1/sys/health" >/dev/null 2>&1 && break; sleep 1; \
	done; \
	curl -sf -X POST -H "X-Vault-Token: $$token" -d '{"type":"transit"}' \
		"$$addr/v1/sys/mounts/transit" >/dev/null; \
	DFBG_VAULT_ADDR="$$addr" DFBG_VAULT_TOKEN="$$token" \
	DFBG_KEY_PROVIDER=vault DFBG_VAULT_TRANSIT_MOUNT=transit DFBG_VAULT_TRANSIT_KEY=dufflebag \
	$(MAKE) test-e2e-terraform

.PHONY: test-smoke
# Deliberately outside the default test target: a browser test is slower and
# fails differently from a unit test (duf-ybp). Chrome comes from the standard
# macOS path unless SMOKE_CHROME points elsewhere (CI passes a browser-action
# path). The test itself stands up Postgres in Docker and the freshly built
# binary, and tears both down.
# The console build is a prerequisite only when this target compiles the server
# itself: a handed-over binary already carries the console it was built with,
# and rebuilding the assets here would not change what that binary serves.
test-smoke: $(if $(DUFFLEBAG_BIN),,build-ui) ## Drive the real console in a real browser against a live stack
	@set -e; work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; \
	$(resolve-server-bin); \
	cd web && { [ -d node_modules ] || npm ci; } && \
	SMOKE_BIN="$$bin" SMOKE_CHROME="$(SMOKE_CHROME)" OSV_STUB_IMAGE="$(OSV_STUB_IMAGE)" \
		node --test tests/smoke/smoke.test.mjs

.PHONY: docs-shots
# Parent-run only: this provisions the live backing stack and launches Chrome.
# It is deliberately independent of both CI and the docs build.
docs-shots: $(if $(DUFFLEBAG_BIN),,build-ui) ## Regenerate console screenshots against seeded data
	@set -e; work=$$(mktemp -d); trap 'rm -rf "$$work"' EXIT; \
	$(resolve-server-bin); \
	docker build -f Containerfile.scanner --target stub -t $(OSV_STUB_IMAGE) .; \
	cd web && { [ -d node_modules ] || npm ci; } && \
	SMOKE_BIN="$$bin" SMOKE_CHROME="$(SMOKE_CHROME)" OSV_STUB_IMAGE="$(OSV_STUB_IMAGE)" \
		node tests/shots/docs-shots.mjs

.PHONY: test-kind
test-kind: ## Validate the single-replica Kubernetes manifests on KIND
	e2e/kubernetes/test-kind.sh

.PHONY: test-backup-restore
test-backup-restore: ## Prove logical PostgreSQL backup and restore on KIND
	e2e/kubernetes/test-backup-restore.sh

HELM_UPDATE_GOLDEN ?= 0
HELM_GOLDEN := deploy/helm/testdata/dufflebag.golden.yaml

.PHONY: helm-lint
helm-lint: ## Lint and verify the self-contained Helm chart
	@set -e; rendered=$$(mktemp); status=0; trap 'rm -f "$$rendered"' EXIT; \
		helm lint deploy/helm/dufflebag || status=1; \
		helm template dufflebag deploy/helm/dufflebag --namespace dufflebag > "$$rendered" || status=1; \
		if [ "$(HELM_UPDATE_GOLDEN)" = 1 ]; then \
			cp "$$rendered" "$(HELM_GOLDEN)"; \
		else \
			diff -u "$(HELM_GOLDEN)" "$$rendered" || status=1; \
		fi; \
		deploy/helm/assert-rendered.sh || status=1; \
		exit $$status

.PHONY: test-helm-kind
test-helm-kind: ## Install the encrypted self-contained Helm stack on KIND
	deploy/helm/test-helm-kind.sh

.PHONY: test-packer
# This direct invocation is LOCAL ONLY: it requires the lab CA, provisioned
# hostname certificate, local DNS entry, Packer, and Docker. CI runs the same
# gate via test-packer-ci with a CA minted inside the run. A machine without
# the lab setup may opt out explicitly with TEST_PACKER_SKIP=1; the skip is
# printed so it cannot be mistaken for coverage.
test-packer: ## Drive stock Packer's hcp-sbom provisioner against a live TLS stack (local only)
	@set -e; \
	if [ "$(TEST_PACKER_SKIP)" = "1" ]; then \
		echo "SKIP test-packer: TEST_PACKER_SKIP=1 (local lab TLS/Packer gate; CI runs test-packer-ci instead)"; \
		exit 0; \
	fi; \
	work=$$(mktemp -d); \
	trap 'chmod -R u+w "$$work" 2>/dev/null || true; rm -rf "$$work"' EXIT; \
	$(resolve-server-bin); \
	$(SCANNER_DISABLED_ENV) E2E_PACKER_BIN="$$bin" E2E_PACKER_WORK="$$work" \
	E2E_PACKER_TEMPLATE="$(CURDIR)/e2e/packer/local-sbom.pkr.hcl" \
	E2E_PACKER_CHILD_TEMPLATE="$(CURDIR)/e2e/packer/local-child.pkr.hcl" \
	E2E_PACKER_CLI="$(PACKER_E2E_PACKER)" E2E_PACKER_DOCKER="$(PACKER_E2E_DOCKER)" \
	E2E_PACKER_EXPECT_VERSION="$(PACKER_E2E_EXPECT_VERSION)" \
	E2E_PACKER_HOSTNAME="$(PACKER_E2E_HOSTNAME)" \
	E2E_PACKER_CERT_FILE="$(PACKER_E2E_CERT_FILE)" \
	E2E_PACKER_KEY_FILE="$(PACKER_E2E_KEY_FILE)" \
	E2E_PACKER_CA_FILE="$(PACKER_E2E_CA_FILE)" \
	E2E_PACKER_BASE_IMAGE="$(PACKER_E2E_IMAGE)" \
		node --test e2e/packer/e2e.test.mjs

# The lab-independent form of the gate above, and what CI runs. Everything the
# lab used to supply is created inside the run: the CA and certificate come from
# e2e/support/mint-tls.sh, and the hostname is loopback by definition, so no DNS
# record, no hosts entry and no stored secret are involved. That last property is
# what makes the gate safe to run on a fork's pull request.
PACKER_CI_HOSTNAME ?= localhost
PACKER_CI_VAULT    ?= dufflebag-packer-vault

# Go trusts an extra CA through SSL_CERT_FILE on Linux, but on macOS it asks the
# platform verifier, which only reads the keychain — so a CA minted inside the
# run cannot be trusted there. The gate refuses rather than failing three minutes
# later inside Packer with an x509 error, because the harness deliberately strips
# HCP_AUTH_TLS and HCP_API_TLS: proving TLS trust is part of what it proves, and
# an insecure fallback would quietly retire that.
define packer-ci-platform-guard
	if [ "$$(uname -s)" = Darwin ]; then \
		echo "REFUSING test-packer-ci on macOS: Go ignores SSL_CERT_FILE here, so an in-run CA cannot be trusted."; \
		echo "  CI runs this on Linux. Locally, use 'make test-packer' with the lab certificate instead."; \
		exit 1; \
	fi
endef

.PHONY: test-packer-ci
test-packer-ci: ## Run the packer gate against an in-run CA (no lab, unencrypted)
	@set -e; $(packer-ci-platform-guard); \
	tls=$$(mktemp -d); trap 'rm -rf "$$tls"' EXIT; \
	e2e/support/mint-tls.sh "$$tls" $(PACKER_CI_HOSTNAME); \
	$(MAKE) test-packer \
		PACKER_E2E_HOSTNAME=$(PACKER_CI_HOSTNAME) \
		PACKER_E2E_CERT_FILE="$$tls/tls.crt" \
		PACKER_E2E_KEY_FILE="$$tls/tls.key" \
		PACKER_E2E_CA_FILE="$$tls/ca.pem"

.PHONY: test-packer-cloud
test-packer-cloud: $(if $(filter 1,$(CLOUD_SHOTS)),build-ui) ## Verify available cloud, container and local VM Packer builders
	@set -e; \
	available=""; \
	[ -n "$${AWS_ACCESS_KEY_ID:-}" ] && available="$$available aws"; \
	[ -n "$${ARM_CLIENT_ID:-}" ] && available="$$available azure"; \
	command -v "$(PACKER_E2E_DOCKER)" >/dev/null 2>&1 && \
		"$(PACKER_E2E_DOCKER)" info >/dev/null 2>&1 && available="$$available docker" || true; \
	if command -v qemu-system-x86_64 >/dev/null 2>&1 && \
		{ [ "$$(uname -s)" = Darwin ] || [ -e /dev/kvm ]; }; then \
		available="$$available qemu"; \
	fi; \
	if [ -z "$$available" ]; then \
		echo "SKIP test-packer-cloud: no source available (checked AWS_ACCESS_KEY_ID, ARM_CLIENT_ID, Docker CLI/daemon, qemu-system-x86_64 and KVM/HVF)"; \
		exit 0; \
	fi; \
	if [ "$$(uname -s)" = Darwin ]; then \
		echo "REFUSING test-packer-cloud on macOS: Go ignores SSL_CERT_FILE here, so an in-run CA cannot be trusted."; \
		echo "  The cloud verification workflow runs this gate on Linux."; \
		exit 1; \
	fi; \
	work=$$(mktemp -d); tls=$$(mktemp -d); \
	trap 'rm -rf "$$work" "$$tls"' EXIT; \
	if [ -n "$(CLOUD_JUNIT)" ]; then mkdir -p "$$(dirname "$(CLOUD_JUNIT)")"; fi; \
	e2e/support/mint-tls.sh "$$tls" $(PACKER_CI_HOSTNAME); \
	$(resolve-server-bin); \
	$(SCANNER_DISABLED_ENV) E2E_PACKER_BIN="$$bin" E2E_PACKER_WORK="$$work" \
	E2E_PACKER_TEMPLATE_DIR="$(CURDIR)/e2e/packer" \
	E2E_PACKER_CLI="$(PACKER_E2E_PACKER)" E2E_PACKER_DOCKER="$(PACKER_E2E_DOCKER)" \
	E2E_PACKER_HOSTNAME="$(PACKER_CI_HOSTNAME)" \
	E2E_PACKER_CERT_FILE="$$tls/tls.crt" E2E_PACKER_KEY_FILE="$$tls/tls.key" \
	E2E_PACKER_CA_FILE="$$tls/ca.pem" CLOUD_SHOTS="$(CLOUD_SHOTS)" \
	CLOUD_SHOTS_DIR="$(CLOUD_SHOTS_DIR)" SMOKE_CHROME="$(CLOUD_CHROME)" \
		node --test \
		$(if $(strip $(CLOUD_JUNIT)),--test-reporter spec --test-reporter-destination stdout --test-reporter junit --test-reporter-destination "$(CLOUD_JUNIT)") \
		e2e/packer/cloud.test.mjs

# The encrypted posture needs a key service, not the lab's. A dev-mode Vault in
# a container is the same shape the smoke lane already runs, and the keyring
# code cannot tell the difference: it asks transit to wrap and unwrap either way.
# The namespaced-transit proof stays lab-bound (duf-j6aa) — namespaces are an
# Enterprise feature no OSS container can stand in for.
.PHONY: test-packer-ci-encrypted
test-packer-ci-encrypted: ## Run the packer gate encrypted against a Vault dev container
	@set -e; $(packer-ci-platform-guard); \
	tls=$$(mktemp -d); \
	trap 'rm -rf "$$tls"; $(PACKER_E2E_DOCKER) rm -f $(PACKER_CI_VAULT) >/dev/null 2>&1 || true' EXIT; \
	e2e/support/mint-tls.sh "$$tls" $(PACKER_CI_HOSTNAME); \
	token=$$(openssl rand -hex 16); \
	$(PACKER_E2E_DOCKER) rm -f $(PACKER_CI_VAULT) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) run -d --name $(PACKER_CI_VAULT) \
		-e VAULT_DEV_ROOT_TOKEN_ID="$$token" -p 127.0.0.1::8200 hashicorp/vault:1.17 >/dev/null; \
	port=$$($(PACKER_E2E_DOCKER) port $(PACKER_CI_VAULT) 8200/tcp | head -1 | sed 's/.*://'); \
	addr="http://127.0.0.1:$$port"; \
	for _ in $$(seq 1 60); do \
		curl -sf "$$addr/v1/sys/health" >/dev/null 2>&1 && break; sleep 1; \
	done; \
	curl -sf -X POST -H "X-Vault-Token: $$token" -d '{"type":"transit"}' \
		"$$addr/v1/sys/mounts/transit" >/dev/null; \
	DFBG_VAULT_ADDR="$$addr" DFBG_VAULT_TOKEN="$$token" \
	DFBG_KEY_PROVIDER=vault DFBG_VAULT_TRANSIT_MOUNT=transit DFBG_VAULT_TRANSIT_KEY=dufflebag \
	$(MAKE) test-packer \
		PACKER_E2E_HOSTNAME=$(PACKER_CI_HOSTNAME) \
		PACKER_E2E_CERT_FILE="$$tls/tls.crt" \
		PACKER_E2E_KEY_FILE="$$tls/tls.key" \
		PACKER_E2E_CA_FILE="$$tls/ca.pem"

.PHONY: spec-drift
spec-drift: ## Report when HCP's published specs appear to have changed upstream
	go run ./cmd/spec-drift

.PHONY: schema-compat-build
schema-compat-build: ## Build this release's schema compatibility probe
	go build -o $(SCHEMA_COMPAT) ./cmd/schema-compat

.PHONY: expand-contract
# The self-comparison fallback is legitimate exactly once: the release that
# introduced the probe had no earlier probe to run, so the current one stands in
# and the run passes by construction. Every other route into the fallback is an
# unusable PREVIOUS_REF — all-zeros from github.event.before on a branch's first
# push, a typo, an unfetched sha — and a vacuous pass used to be
# indistinguishable from a real one in CI logs. Hence the noise: the fallback
# shouts on stdout and, under GitHub Actions, annotates the workflow run, so a
# reader cannot mistake a self-comparison for a compatibility result (the same
# class of gate as test-rls-sabotage: a check that can silently prove nothing
# is not a check).
expand-contract: ## Run the previous application release against the new schema
	@work=$$(mktemp -d); \
	trap 'rm -rf "$$work"' EXIT; \
	if git cat-file -e "$(PREVIOUS_REF):cmd/schema-compat/main.go" 2>/dev/null; then \
		git archive "$(PREVIOUS_REF)" | tar -x -C "$$work"; \
		$(MAKE) -C "$$work" schema-compat-build SCHEMA_COMPAT="$$work/previous-app"; \
	else \
		echo "WARNING: expand-contract is SELF-COMPARING — PREVIOUS_REF '$(PREVIOUS_REF)' has no schema probe (unusable ref, or the probe's first release)."; \
		echo "WARNING: the CURRENT probe stands in for the previous release, so this run passes by construction and proves nothing about any earlier release."; \
		if [ -n "$$GITHUB_ACTIONS" ]; then \
			echo "::warning title=expand-contract self-comparison::PREVIOUS_REF '$(PREVIOUS_REF)' is unusable; the schema probe was tested against itself and this pass proves nothing about an earlier release"; \
		fi; \
		$(MAKE) schema-compat-build SCHEMA_COMPAT="$$work/previous-app"; \
	fi; \
	PREVIOUS_APP="$$work/previous-app" go test -tags=integration ./internal/store/postgres \
		-run '^TestPreviousReleaseAgainstNewSchema$$' -count=1

.PHONY: lint
lint: $(GOLANGCI) ## Run linters, including the ADR-0002 layer boundaries
	go vet ./...
	$(GOLANGCI) run ./...

.PHONY: image
image: ## Build the container image locally
	docker build -f Containerfile \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE):$(IMAGE_TAG) .

# --provenance=false --sbom=false are NOT optional (conventions rule 6): buildx
# attestations otherwise wrap the push in an OCI index that hides the labels
# below, and an RC tag never expires. Single-platform for the same reason — a
# manifest list buries per-image labels exactly the way attestations do.
.PHONY: image-push-rc
image-push-rc: ## Build and push an expiring release-candidate image
	docker buildx build -f Containerfile --platform linux/amd64 \
		--provenance=false --sbom=false \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--label quay.expires-after=$(IMAGE_EXPIRES) \
		-t $(IMAGE):$(IMAGE_TAG) --push .

.PHONY: image-push-release
image-push-release: ## Build and push a release image (no expiry)
	docker buildx build -f Containerfile --platform linux/amd64 \
		--provenance=false --sbom=false \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE):$(IMAGE_TAG) --push .

LICENCE_ALLOWLIST := MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0

.PHONY: check-licenses
# The allowlist mirrors the dependency audit of 2026-08-18: every module in
# both graphs carries one of these licences, so this passes today by
# construction. A failure means a dependency changed licence on upgrade —
# investigate the module, do not widen the list to get green.
check-licenses: $(GO_LICENSES) ## Fail on dependency licences outside the allowlist
	$(GO_LICENSES) check ./... --allowed_licenses=$(LICENCE_ALLOWLIST)
	cd contract && $(GO_LICENSES) check ./... --allowed_licenses=$(LICENCE_ALLOWLIST)

SBOM_OUT ?= dufflebag.cdx.json

.PHONY: sbom
# `app` mode, not `mod`: the SBOM describes what the server binary actually
# links, not the whole module graph with its test-only dependencies. The tool
# reads git through go-git, which does not understand linked worktrees or
# shared clones — run it from a plain clone with tags fetched.
sbom: $(CYCLONEDX) ## Write a CycloneDX SBOM for the server binary
	$(CYCLONEDX) app -json -licenses -output $(SBOM_OUT) -main cmd/dufflebag .

.PHONY: check-markers
# The history was rewritten once to strip AI-tooling markers from tracked files
# and they crept back (duf-qlo3.3); the gate, not the sweep, is what keeps the
# tree clean. The vendor names are assembled from fragments so this recipe never
# contains the strings it hunts — which means the Makefile is scanned like every
# other tracked file, and a marker added even here is caught rather than hidden
# behind a path exclusion.
check-markers: ## Fail on AI-tooling markers in tracked files
	@p="cla""ude|anthro""pic|co""dex|open""ai|chat""gpt|gpt-[0-9o]|copi""lot|gem""ini|wind""surf|ai-gen""erated|co-auth""ored-by"; \
	if hits=$$(git grep -n -i -E "$$p" -- .); then \
		echo "$$hits"; \
		echo "FAIL: AI-tooling markers in tracked files — remove them (duf-qlo3.3)"; \
		exit 1; \
	fi; \
	echo "no AI-tooling markers in tracked files"

.PHONY: demo-up
# Stands up a LONG-LIVED instance to browse, and CLAIMS IT before it can be
# reached. /sys/init is one-shot and ADR-0012 accepts that whoever reaches it
# first owns the deployment, so an unclaimed instance must never be exposed —
# demo-expose refuses to tunnel to one.
# Any server from a previous run is cleared first — the current container and
# any legacy host-process binary from an older demo — since either holds
# $(DEMO_PORT) and points at backing containers this target is about to
# recreate, so /sys/init would otherwise reach a stale instance.
# Unlike every CI gate, the demo deliberately sends versioned purl-derived
# package metadata to live api.osv.dev and therefore requires internet egress.
demo-up: ## Stand up a long-lived demo instance and claim it (DEMO_CLAIM=0 leaves it unclaimed)
	@set -e; \
	command -v $(PACKER_E2E_DOCKER) >/dev/null || { echo "FAIL demo-up: docker is required"; exit 1; }; \
	test -r "$(PACKER_E2E_CERT_FILE)" || { echo "FAIL demo-up: no TLS certificate at $(PACKER_E2E_CERT_FILE)"; exit 1; }; \
	test -r "$(PACKER_E2E_CA_FILE)" || { echo "FAIL demo-up: no CA chain at $(PACKER_E2E_CA_FILE)"; exit 1; }; \
	mkdir -p "$(DEMO_DIR)"; \
	pkill -f "$(DEMO_DIR)/dufflebag" 2>/dev/null || true; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_SERVER_CONTAINER) >/dev/null 2>&1 || true; \
	rm -f "$(DEMO_DIR)/server.pid"; \
	rm -f "$(DEMO_DIR)/root.json" "$(DEMO_DIR)/builder.env" \
		"$(DEMO_DIR)/organization_id" "$(DEMO_DIR)/project_id"; \
	$(PACKER_E2E_DOCKER) pull $(DEMO_IMAGE); \
	$(PACKER_E2E_DOCKER) network create $(DEMO_NET) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) run -d --name $(DEMO_CONTAINER) --network $(DEMO_NET) --network-alias postgres -e POSTGRES_PASSWORD=postgres \
		-e POSTGRES_DB=dufflebag -p 127.0.0.1::5432 postgres:17-alpine >/dev/null; \
	pg=$$($(PACKER_E2E_DOCKER) port $(DEMO_CONTAINER) 5432/tcp | head -1 | sed 's/.*://'); \
	for _ in $$(seq 1 60); do $(PACKER_E2E_DOCKER) exec $(DEMO_CONTAINER) pg_isready -U postgres -d dufflebag >/dev/null 2>&1 && break; sleep 1; done; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_S3_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) run -d --name $(DEMO_S3_CONTAINER) --network $(DEMO_NET) --network-alias s3 -p 127.0.0.1::8000 $(DEMO_S3_IMAGE) >/dev/null; \
	s3=$$($(PACKER_E2E_DOCKER) port $(DEMO_S3_CONTAINER) 8000/tcp | head -1 | sed 's/.*://'); \
	echo "waiting for Ceph (this takes a minute or so)"; \
	ready=; \
	for _ in $$(seq 1 150); do \
		state=$$($(PACKER_E2E_DOCKER) inspect -f '{{.State.Health.Status}}' $(DEMO_S3_CONTAINER) 2>/dev/null || echo starting); \
		[ "$$state" = healthy ] && { ready=yes; break; }; \
		sleep 2; \
	done; \
	[ -n "$$ready" ] || { echo "FAIL demo-up: Ceph never reported healthy"; exit 1; }; \
	$(PACKER_E2E_DOCKER) exec $(DEMO_S3_CONTAINER) radosgw-admin user create --uid=dufflebag-demo \
		--display-name='dufflebag demo' --access-key=demoaccess --secret-key=demosecret >/dev/null; \
	$(PACKER_E2E_DOCKER) cp e2e/support/create-bucket.py $(DEMO_S3_CONTAINER):/tmp/create-bucket.py; \
	$(PACKER_E2E_DOCKER) exec $(DEMO_S3_CONTAINER) python3 /tmp/create-bucket.py \
		demoaccess demosecret $(DEMO_S3_BUCKET) >/dev/null; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_VAULT_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) run -d --name $(DEMO_VAULT_CONTAINER) --network $(DEMO_NET) --network-alias vault \
		-e VAULT_DEV_ROOT_TOKEN_ID=$(DEMO_VAULT_TOKEN) -p 127.0.0.1::8200 \
		$(DEMO_VAULT_IMAGE) >/dev/null; \
	vault_port=$$($(PACKER_E2E_DOCKER) port $(DEMO_VAULT_CONTAINER) 8200/tcp | head -1 | sed 's/.*://'); \
	for _ in $$(seq 1 60); do \
		curl -sf "http://127.0.0.1:$$vault_port/v1/sys/health" >/dev/null 2>&1 && break; sleep 1; \
	done; \
	curl -sf -X POST "http://127.0.0.1:$$vault_port/v1/sys/mounts/transit" \
		-H "X-Vault-Token: $(DEMO_VAULT_TOKEN)" -d '{"type":"transit"}' >/dev/null || { \
		echo "FAIL demo-up: could not mount the transit engine"; exit 1; }; \
	$(PACKER_E2E_DOCKER) run --rm --network $(DEMO_NET) \
		-e DFBG_DATABASE_URL="postgres://postgres:postgres@postgres:5432/dufflebag?sslmode=disable" \
		$(DEMO_IMAGE) migrate >/dev/null; \
	$(PACKER_E2E_DOCKER) exec $(DEMO_CONTAINER) psql -q -v ON_ERROR_STOP=1 -U postgres -d dufflebag \
		-c "CREATE ROLE dufflebag_app LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS" \
		-c 'GRANT USAGE ON SCHEMA public TO dufflebag_app' \
		-c 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app' >/dev/null; \
	$(PACKER_E2E_DOCKER) run -d --name $(DEMO_SERVER_CONTAINER) --network $(DEMO_NET) \
		-p 127.0.0.1:$(DEMO_PORT):$(DEMO_PORT) \
		-v "$(PACKER_E2E_CERT_FILE)":/tls/tls.crt:ro -v "$(PACKER_E2E_KEY_FILE)":/tls/tls.key:ro \
		-e DFBG_DATABASE_URL="postgres://dufflebag_app:app@postgres:5432/dufflebag?sslmode=disable" \
		-e DFBG_HTTP_ADDR=0.0.0.0:$(DEMO_PORT) \
		-e DFBG_TLS_CERT_FILE=/tls/tls.crt -e DFBG_TLS_KEY_FILE=/tls/tls.key \
		-e DFBG_KEY_PROVIDER=vault -e DFBG_VAULT_ADDR="http://vault:8200" -e DFBG_VAULT_TOKEN=$(DEMO_VAULT_TOKEN) \
		-e DFBG_OBJECT_STORAGE_ENDPOINT="http://s3:8000" -e DFBG_OBJECT_STORAGE_REGION=us-east-1 \
		-e DFBG_OBJECT_STORAGE_BUCKET=$(DEMO_S3_BUCKET) \
		-e DFBG_OBJECT_STORAGE_ACCESS_KEY=demoaccess -e DFBG_OBJECT_STORAGE_SECRET_KEY=demosecret \
		-e DFBG_SCANNER_ADAPTER=osv -e DFBG_SCANNER_ENDPOINT=https://api.osv.dev \
		$(DEMO_IMAGE) >/dev/null; \
	for _ in $$(seq 1 60); do curl -s --cacert "$(PACKER_E2E_CA_FILE)" "https://$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)/sys/health" >/dev/null 2>&1 && break; sleep 1; done; \
	base="https://$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)"; \
	if [ "$(DEMO_CLAIM)" != "1" ]; then \
		echo "UNCLAIMED $$base"; \
		echo "claim it via the console wizard, or: curl -sX POST --cacert $(PACKER_E2E_CA_FILE) $$base/sys/init -d '{}'"; \
		echo "do NOT expose an unclaimed instance: whoever reaches /sys/init first owns it"; \
		exit 0; \
	fi; \
	curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST "$$base/sys/init" \
		-H 'content-type: application/json' -d '{}' > "$(DEMO_DIR)/root.json"; \
	chmod 600 "$(DEMO_DIR)/root.json"; \
	id=$$(sed -n 's/.*"client_id":"\([^"]*\)".*/\1/p' "$(DEMO_DIR)/root.json"); \
	secret=$$(sed -n 's/.*"client_secret":"\([^"]*\)".*/\1/p' "$(DEMO_DIR)/root.json"); \
	test -n "$$id" -a -n "$$secret" || { echo "FAIL demo-up: /sys/init returned no credentials"; exit 1; }; \
	token=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST "$$base/oauth2/token" \
		-u "$$id:$$secret" -H 'content-type: application/x-www-form-urlencoded' \
		-d 'grant_type=client_credentials&audience=https://api.hashicorp.cloud' \
		| sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'); \
	test -n "$$token" || { echo "FAIL demo-up: could not exchange the root credentials"; exit 1; }; \
	org=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST "$$base/api/v1/organizations" \
		-H "authorization: Bearer $$token" -H 'content-type: application/json' \
		-d '{"name":"$(DEMO_ORG)"}' | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); \
	test -n "$$org" || { echo "FAIL demo-up: could not create the organisation"; exit 1; }; \
	project=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST \
		"$$base/api/v1/organizations/$$org/projects" \
		-H "authorization: Bearer $$token" -H 'content-type: application/json' \
		-d '{"name":"$(DEMO_PROJECT)"}' | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); \
	test -n "$$project" || { echo "FAIL demo-up: could not create the project"; exit 1; }; \
	builder=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST "$$base/api/v1/principals" \
		-H "authorization: Bearer $$token" -H 'content-type: application/json' \
		-d "{\"name\":\"demo-builder\",\"role\":\"builder\",\"organization_id\":\"$$org\",\"project_id\":\"$$project\"}"); \
	builder_id=$$(printf '%s' "$$builder" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); \
	builder_client=$$(printf '%s' "$$builder" | sed -n 's/.*"client_id":"\([^"]*\)".*/\1/p'); \
	test -n "$$builder_id" -a -n "$$builder_client" || { echo "FAIL demo-up: could not create the builder principal"; exit 1; }; \
	builder_secret=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST \
		"$$base/api/v1/principals/$$builder_id/secrets" \
		-H "authorization: Bearer $$token" \
		| sed -n 's/.*"secret":"\([^"]*\)".*/\1/p'); \
	test -n "$$builder_secret" || { echo "FAIL demo-up: could not issue the builder secret"; exit 1; }; \
	printf 'HCP_CLIENT_ID=%s\nHCP_CLIENT_SECRET=%s\n' "$$builder_client" "$$builder_secret" > "$(DEMO_DIR)/builder.env"; \
	chmod 600 "$(DEMO_DIR)/builder.env"; \
	printf '%s\n' "$$org" > "$(DEMO_DIR)/organization_id"; \
	printf '%s\n' "$$project" > "$(DEMO_DIR)/project_id"; \
	echo "CLAIMED $$base"; \
	echo "root credentials: $(DEMO_DIR)/root.json"; \
	echo "builder credentials: $(DEMO_DIR)/builder.env (demo-publish reads these)"; \
	echo "organisation:     $(DEMO_ORG) ($$org)"; \
	echo "project:          $(DEMO_PROJECT) ($$project)"; \
	echo "next: make demo-expose DEMO_NGROK_URL=<your reserved ngrok url>"

.PHONY: demo-expose
# Refuses on a missing ngrok and on an UNCLAIMED instance, in that order. A
# tunnel to an uninitialized instance hands the deployment to whoever finds it.
demo-expose: ## Expose the claimed demo instance over ngrok (refuses if unclaimed)
	@set -e; \
	command -v $(DEMO_NGROK) >/dev/null || { \
		echo "FAIL demo-expose: $(DEMO_NGROK) is not on PATH — install it or set DEMO_NGROK"; exit 1; }; \
	test -n "$(DEMO_NGROK_URL)" || { \
		echo "FAIL demo-expose: set DEMO_NGROK_URL to your reserved ngrok hostname"; exit 1; }; \
	mkdir -p "$(DEMO_DIR)"; \
	base="https://$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)"; \
	health=$$(curl -sS --cacert "$(PACKER_E2E_CA_FILE)" "$$base/sys/health" 2>/dev/null) || { \
		echo "FAIL demo-expose: nothing serving at $$base — run make demo-up"; exit 1; }; \
	test -n "$$health" || { \
		echo "FAIL demo-expose: nothing serving at $$base — run make demo-up"; exit 1; }; \
	echo "$$health" | grep -q '"initialized":true' || { \
		echo "FAIL demo-expose: instance is NOT claimed ($$health). Exposing it would let anyone claim it (ADR-0012)."; exit 1; }; \
	mkdir -p "$(DEMO_DIR)"; \
	nohup $(DEMO_NGROK) http --url=$(DEMO_NGROK_URL) "$$base" > "$(DEMO_DIR)/ngrok.log" 2>&1 & \
	echo $$! > "$(DEMO_DIR)/ngrok.pid"; sleep 5; \
	curl -sSf --max-time 10 "https://$(DEMO_NGROK_URL)/sys/health" >/dev/null || { \
		echo "FAIL demo-expose: tunnel did not serve; see $(DEMO_DIR)/ngrok.log"; exit 1; }; \
	echo "EXPOSED https://$(DEMO_NGROK_URL)"

.PHONY: demo-publish
demo-publish: ## Publish the demo corpus (wizard-claimed: supply the HCP_* env from the Instance page)
	@set -e; \
	command -v $(PACKER_E2E_PACKER) >/dev/null || { echo "FAIL demo-publish: packer is required"; exit 1; }; \
	command -v $(PACKER_E2E_DOCKER) >/dev/null || { echo "FAIL demo-publish: docker is required"; exit 1; }; \
	if [ -z "$$HCP_CLIENT_ID" ] && [ -r "$(DEMO_DIR)/builder.env" ]; then \
		. "$(DEMO_DIR)/builder.env"; export HCP_CLIENT_ID HCP_CLIENT_SECRET; \
	fi; \
	test -n "$$HCP_CLIENT_ID" || { echo "FAIL demo-publish: HCP_CLIENT_ID is required — make demo-up mints $(DEMO_DIR)/builder.env"; exit 1; }; \
	test -n "$$HCP_CLIENT_SECRET" || { echo "FAIL demo-publish: HCP_CLIENT_SECRET is required"; exit 1; }; \
	org="$${HCP_ORGANIZATION_ID:-}"; \
	if [ -z "$$org" ] && [ -r "$(DEMO_DIR)/organization_id" ]; then org=$$(cat "$(DEMO_DIR)/organization_id"); fi; \
	test -n "$$org" || { echo "FAIL demo-publish: set HCP_ORGANIZATION_ID, or run make demo-up (writes $(DEMO_DIR)/organization_id)"; exit 1; }; \
	project="$${HCP_PROJECT_ID:-}"; \
	if [ -z "$$project" ] && [ -r "$(DEMO_DIR)/project_id" ]; then project=$$(cat "$(DEMO_DIR)/project_id"); fi; \
	test -n "$$project" || { echo "FAIL demo-publish: set HCP_PROJECT_ID, or run make demo-up (writes $(DEMO_DIR)/project_id)"; exit 1; }; \
	test -r "$(PACKER_E2E_CA_FILE)" || { echo "FAIL demo-publish: no CA chain at $(PACKER_E2E_CA_FILE)"; exit 1; }; \
	base="https://$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)"; \
	curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" "$$base/sys/health" >/dev/null || { \
		echo "FAIL demo-publish: nothing serving at $$base — run make demo-up"; exit 1; }; \
	channel_client="$$HCP_CLIENT_ID"; channel_secret="$$HCP_CLIENT_SECRET"; channel_credentials=provided; \
	if [ -r "$(DEMO_DIR)/root.json" ]; then \
		channel_client=$$(sed -n 's/.*"client_id":"\([^"]*\)".*/\1/p' "$(DEMO_DIR)/root.json"); \
		channel_secret=$$(sed -n 's/.*"client_secret":"\([^"]*\)".*/\1/p' "$(DEMO_DIR)/root.json"); \
		channel_credentials=root; \
		test -n "$$channel_client" -a -n "$$channel_secret" || { \
			echo "FAIL demo-publish: $(DEMO_DIR)/root.json contains no root credentials"; exit 1; }; \
	fi; \
	channel_token=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" -X POST "$$base/oauth2/token" \
		-u "$$channel_client:$$channel_secret" -H 'content-type: application/x-www-form-urlencoded' \
		-d 'grant_type=client_credentials&audience=https://api.hashicorp.cloud' \
		| sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'); \
	test -n "$$channel_token" || { echo "FAIL demo-publish: could not exchange channel credentials"; exit 1; }; \
	packer_home="$(DEMO_DIR)/packer-home"; \
	mkdir -p "$$packer_home"; \
	epoch=$$(date +%s); \
	run_label="dufflebag-sbom-demo-$$epoch"; \
	$(PACKER_E2E_DOCKER) pull "$(PACKER_E2E_IMAGE)" >/dev/null; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) init e2e/packer/demo-sbom.pkr.hcl; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) init e2e/packer/demo-child.pkr.hcl; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) init e2e/packer/demo-distro.pkr.hcl; \
	build_distro() { \
		distro_bucket="$$1"; distro_image="$$2"; \
		env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
			HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
			HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
			$(PACKER_E2E_PACKER) build -color=false \
			-var "base_image=$$distro_image" -var "bucket_name=$$distro_bucket" -var "run_label=$$run_label" \
			e2e/packer/demo-distro.pkr.hcl || { \
				echo "FAIL demo-publish: $$distro_bucket build failed"; exit 1; }; \
	}; \
	create_release_channel() { \
		release_bucket="$$1"; \
		latest=$$(curl -sSf --cacert "$(PACKER_E2E_CA_FILE)" \
			-H "authorization: Bearer $$channel_token" \
			"$$base/packer/2023-01-01/organizations/$$org/projects/$$project/buckets/$$release_bucket/channels/latest"); \
		v1_fingerprint=$$(printf '%s' "$$latest" | sed -n 's/.*"fingerprint":"\([^"]*\)".*/\1/p'); \
		test -n "$$v1_fingerprint" || { \
			echo "FAIL demo-publish: $$release_bucket latest has no v1 fingerprint"; exit 1; }; \
		release_result=$$(curl -sS --cacert "$(PACKER_E2E_CA_FILE)" -X POST \
			-H "authorization: Bearer $$channel_token" -H 'content-type: application/json' \
			-d "{\"name\":\"release\",\"restricted\":false,\"version_fingerprint\":\"$$v1_fingerprint\"}" \
			-w '\n%{http_code}' \
			"$$base/packer/2023-01-01/organizations/$$org/projects/$$project/buckets/$$release_bucket/channels"); \
		release_status=$$(printf '%s\n' "$$release_result" | tail -n 1); \
		if [ "$$release_status" = 200 ]; then \
			echo "pinned $$release_bucket release to v1 $$v1_fingerprint"; \
		elif [ "$$release_status" = 403 ] && [ "$$channel_credentials" = provided ]; then \
			echo "SKIP demo-publish: $$release_bucket release channel needs publisher+; provided HCP_CLIENT principal was refused"; \
		else \
			echo "FAIL demo-publish: $$release_bucket release channel answered HTTP $$release_status"; exit 1; \
		fi; \
	}; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) build -color=false \
		-var "base_image=$(PACKER_E2E_IMAGE)" -var "run_label=$$run_label" \
		e2e/packer/demo-sbom.pkr.hcl; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) build -color=false -var "run_label=$$run_label" \
		e2e/packer/demo-child.pkr.hcl; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) build -color=false \
		-var "base_image=$(PACKER_E2E_IMAGE)" -var "run_label=$$run_label" \
		e2e/packer/demo-sbom.pkr.hcl; \
	env HOME="$$packer_home" SSL_CERT_FILE="$(PACKER_E2E_CA_FILE)" HCP_AUTH_URL="$$base" \
		HCP_API_ADDRESS="$(PACKER_E2E_HOSTNAME):$(DEMO_PORT)" \
		HCP_ORGANIZATION_ID="$$org" HCP_PROJECT_ID="$$project" HCP_SKIP_STATUS_CHECK=true \
		$(PACKER_E2E_PACKER) build -color=false \
		-var "base_image=$(PACKER_E2E_IMAGE)" -var "run_label=$$run_label" \
		e2e/packer/demo-sbom.pkr.hcl; \
	echo "PUBLISHED parent, child, parent, parent builds (run label=$$run_label)"; \
	for spec in $(DEMO_DISTROS); do \
		bucket=$${spec%%=*}; image=$${spec#*=}; \
		echo "building $$bucket from $$image"; \
		$(PACKER_E2E_DOCKER) pull "$$image" >/dev/null; \
		build_distro "$$bucket" "$$image"; \
		case "$$bucket" in \
			demo-ubi|demo-ubuntu) \
				create_release_channel "$$bucket"; \
				build_distro "$$bucket" "$$image"; \
				;; \
		esac; \
	done; \
	echo "PUBLISHED distro buckets: $(DEMO_DISTROS)"

.PHONY: demo-down
demo-down: ## Tear down the demo stack and its tunnel
	@set -e; \
	[ -f "$(DEMO_DIR)/ngrok.pid" ] && kill "$$(cat $(DEMO_DIR)/ngrok.pid)" 2>/dev/null || true; \
	[ -f "$(DEMO_DIR)/server.pid" ] && kill "$$(cat $(DEMO_DIR)/server.pid)" 2>/dev/null || true; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_SERVER_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_S3_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) rm -f $(DEMO_VAULT_CONTAINER) >/dev/null 2>&1 || true; \
	$(PACKER_E2E_DOCKER) network rm $(DEMO_NET) >/dev/null 2>&1 || true; \
	rm -f "$(DEMO_DIR)/ngrok.pid" "$(DEMO_DIR)/server.pid"; \
	echo "demo stack down"
