# dufflebag

> **Independent community project.** dufflebag is not maintained, supported or
> endorsed by IBM or HashiCorp. HCP and Packer are their products; dufflebag
> implements a client-observed API contract and is not affiliated with either
> company.

*Chuck it in the bag.*

[Packer](https://developer.hashicorp.com/packer/docs) builds machine images,
but that's where its responsibilities end. In isolation, it is not designed
to answer questions such as:

- Which image should production be using right now?
- Is the image that was deployed into staging the same one that production
  is about to get?
- When a CVE lands in a base image, which of your images carry it - and how
  do you stop the next deployment from using them?

Answering those questions takes a registry - one record of every build, with
named channels that say which version each environment should consume,
instead of image IDs pasted into tfvars files and wikis. dufflebag is an
independent, self-hosted implementation of the
[HCP Packer APIs](https://developer.hashicorp.com/hcp/api-docs/packer)
behind Packer's
[HCP registry integration](https://developer.hashicorp.com/packer/docs/hcp).
The stock community `packer` binary publishes to an endpoint you run instead
of `api.hashicorp.cloud`, and the Packer resources and data sources in
[`terraform-provider-hcp`](https://registry.terraform.io/providers/hashicorp/hcp/latest/docs)
work against the same endpoint.

An embedded [PatternFly](https://www.patternfly.org/) console provides first-run
initialisation, registry browsing, service-principal management, audit
configuration, encryption and key-rotation status, and vulnerability findings
when a scanner is configured.

## Architectural boundaries

**Two API planes.** The frozen compatibility plane serves the externally owned
auth, resource-manager and registry contracts. Its wire models are generated
from vendored Swagger specifications. Handlers preserve client-observed
behaviour, including its accidents. The platform plane is dufflebag's own
OpenAPI contract for tenancy, identity, audit and console sessions. See the
[architecture reference](https://benemon.github.io/dufflebag/components/architecture) and the
[compatibility reference](https://benemon.github.io/dufflebag/reference/compatibility).

**Metadata, not machine images.** dufflebag records buckets, versions, builds,
channels, artefact identifiers and client-reported SBOMs. It does not store or
scan the machine images Packer creates. When a scanner adapter is configured it
checks the client-reported SBOM package inventory against an external
vulnerability service. It never certifies that an SBOM describes the image it
was reported against.

**Existing clients are the interfaces.** There is no dufflebag client CLI. The
server binary's only subcommand is `migrate`. Packer
publishes build metadata, Terraform manages supported registry resources, and
the console covers bootstrap and operational workflows. The supported
Terraform surface is recorded in the
[compatibility reference](https://benemon.github.io/dufflebag/reference/compatibility).

## Prerequisites

The [installation page](https://benemon.github.io/dufflebag/quick-start/installation)
covers runtime prerequisites, optional backing services and their availability trade-offs.

## Getting started

Follow [Installation](https://benemon.github.io/dufflebag/quick-start/installation)
to install an instance, then [Bootstrap](https://benemon.github.io/dufflebag/quick-start/bootstrap)
to initialise it and point stock Packer and Terraform clients at it.

## Configuration reference

The [deployment environment reference](https://benemon.github.io/dufflebag/quick-start/installation#configuration-reference)
lists server, backing-service, scanner and client-redirection variables.

## Storage and security

The [architecture reference](https://benemon.github.io/dufflebag/components/architecture)
covers tenant isolation, audit availability and SBOM custody.

## Development and testing

Development uses Go 1.26.4 and npm. The local integration,
browser and Packer lanes also require Docker. The tested Packer lane uses stock
Packer 1.16.0, while the compatibility scope starts at Packer 1.15.4. The
Terraform lane pins Terraform 1.14.7 and `terraform-provider-hcp` 0.112.0. The
provider compatibility floor is 0.84.0.

```sh
make build              # web console and all Go packages
make test               # Go and web unit tests
make test-integration   # PostgreSQL integration tests via testcontainers
make test-contract      # generated hcp-sdk-go client against a running server
make test-e2e-terraform # real Terraform CLI and provider against a live stack
make test-smoke         # real browser and console against PostgreSQL and Ceph
make test-packer        # stock Packer and hcp-sbom against PostgreSQL and Ceph
make test-scanner       # scanner against recorded fixtures on a network with no egress
make test-rls-sabotage  # prove the tenant-isolation alarm fires
make lint               # go vet and golangci-lint
```

`make test-packer` as shown drives the lab CA, hostname certificate and DNS
entry, so that invocation is local-only. CI runs the same gate as
`make test-packer-ci` (and `-encrypted`) against a CA minted inside the run.

The manual-only `cloud-verify` workflow proves documented registry claims
against available AWS, Azure and Docker builders. Missing cloud credentials
skip only those cloud sources, and the lane skips cleanly when no source is
available. Solo buckets check each platform's completed version, artifact
identity, region and managed `latest` assignment. The joint bucket checks
that one version completes across all three platforms and receives one
managed `latest` assignment.

Dufflebag registers artifacts from any HCP Packer-ready builder - cloud and
container images alike - including multi-platform buckets where one version
spans them. Stock Packer publishes registry metadata only for builders that
implement it; the QEMU builder, for example, does not, and that limit is the
client's, not the registry's.

Run `make help` for generation, schema-compatibility, demo and other targets.

## Clean-room development

dufflebag is a clean-room reimplementation. The closed server implementation
has not been read. Compatibility evidence comes from public specifications and
documentation, open client and SDK source, and observed traffic from stock
clients.

The working rule is equally important: use `hcp-sdk-go` directly where
appropriate, but never copy source from `packer/internal/hcp`. Record the
contract that client code implies and cite it. The full position and working
rules are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

dufflebag is licensed under the [Mozilla Public License 2.0](LICENSE).
