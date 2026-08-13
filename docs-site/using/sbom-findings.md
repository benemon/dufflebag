# SBOMs and findings

A build can carry one or more SBOMs — software bills of materials — uploaded
while the build runs. dufflebag stores the documents, projects a package
inventory out of them, and, when a scanner is configured, attributes
vulnerability findings to those packages. The full wire contract is in the
[compatibility reference](../reference/compatibility.md).

SBOM storage requires an S3-compatible object store; without one, upload and
download answer 503 while everything else keeps working.

## Uploading from Packer

Add HashiCorp's `hcp-sbom` provisioner to a build that publishes to the
registry; it uploads the document to the build during the run. The rules that
matter:

- Uploads are only accepted **while the build is running** — outside that
  window the request is refused with
  `This build's status isn't Running, so sboms can not be uploaded`.
- Any upload error **fails the Packer build** — this is the stock client's
  behaviour, not dufflebag's choice.
- SPDX JSON and CycloneDX JSON are both supported. The document travels
  zstd-compressed; the original bytes are stored verbatim (sealed at rest on
  encrypted deployments).
- The SBOM name is optional. An unnamed upload is stored under the build
  fingerprint — a recorded divergence from HCP Packer, which mints a random
  identifier; nothing in the supported client surface parses the name either
  way.

## Reading and downloading

Any `reader` can list a build's SBOMs and download them — from the build
screen's SBOM card in the console, or via the API. The download always serves
the **decompressed** document as `<name>.json`, whatever format was uploaded:
compression is transport, not contract.

## The package inventory

Each SBOM is parsed at upload, in the same transaction that stores it. The
build's packages read returns one row per `(name, version, purl)` with the
SBOMs it came from; the console's build screen shows the same inventory on
its **Packages** tab. A document that cannot be parsed stays stored with an
explicit unparseable status, and the packages read says so rather than
returning an empty — and falsely clean-looking — list.

One honest boundary: package names, versions and purls are
**client-reported**. dufflebag records and attributes the assertion; it does
not certify the SBOM against the image.

## Vulnerability findings

Scanning is optional and off by default — an operator enables the OSV adapter
(`DFBG_SCANNER_ADAPTER=osv`), which queries by package name and version
derived from purls; the SBOM document itself never leaves the deployment. See
the
[deployment guide](../deployment/operations.md#vulnerability-scanning-optional)
for the operator contract.

The absence rule is deliberate everywhere findings appear: **no scan means
absent, never empty**. An unscanned build has no `vuln_details` at all, the
bucket-level vulnerability reads answer not-found rather than an empty
success, and the console never describes an unscanned state as clean. Only a
current successful scan produces findings — including a genuinely empty
findings list when the scan found nothing — and a failed newer scan attempt
does not erase the previous successful run's findings.

With a scanner configured:

- Build packages carry their findings, and responses are attributed with
  `Dufflebag-Scan-*` headers naming the adapter, engine, database revision
  and coverage counts.
- Bucket-level reads aggregate across builds: a vulnerability summary,
  packages-with-vulnerabilities, and the flat vulnerability list — all
  `reader` operations.
- The console's package tables show severity counts, and rows with findings
  expand to the individual findings — rows without findings have nothing to
  open.

When findings warrant taking an image out of circulation, that is what
[revocation](./revocation-channels.md) is for: revoke the version, let the
channels roll back, and consumers stop resolving it.

## Where to go next

- [Compatibility reference](../reference/compatibility.md)
  — the SBOM wire contract and package projection rules.
- [Deployment guide](../deployment/index.md)
  — object storage and scanner configuration.
