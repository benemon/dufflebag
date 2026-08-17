# Builds

A build is one component of a version — created running by `packer build`,
completing with its artifacts. This page covers the build screen, SBOMs, the
package inventory and vulnerability findings.

## The build screen

A build shows its status history, artifacts and SBOMs. The **Packages** tab
shows the package inventory projected from its SBOMs, with findings when a
scanner is configured.

The **Packer runner environment** card surfaces what the build reported
about the machine and invocation that produced it: the Packer and plugin
versions, runner OS and architecture, the template path, and — only when the
build set them — the debug/force flags, `-only`/`-except` selections,
variable files and variables, plus the copyable run UUID. Rows the build did
not report are absent rather than blank, and a build that reported nothing
shows no card.

![Dufflebag build screen showing the package inventory from an uploaded SBOM](/screenshots/build.png)

## SBOMs

A build can carry one or more software bills of materials (SBOMs) uploaded
while the build runs. Dufflebag stores the documents and projects a package
inventory from them.

::: warning
SBOM storage requires an S3-compatible
[object store](../components/object-storage.md). Without one, upload and
download return 503. Other operations continue to work.
:::

### Uploading from Packer

Prerequisites: A Packer build that publishes to the registry and a configured
S3-compatible object store.

1. Add HashiCorp's `hcp-sbom` provisioner to the build. It uploads the
   document to the build during the run.

Uploads are accepted only while the build is running. Outside that window,
the request is refused with
`This build's status isn't Running, so sboms can not be uploaded`.

::: warning
Any upload error fails the Packer build. This is the stock client's
behavior, not a choice made by dufflebag.
:::

SPDX JSON and CycloneDX JSON are supported. The document is transported with
zstd compression. Dufflebag stores the original bytes verbatim and seals
them at rest on encrypted deployments.

The SBOM name is optional. An unnamed upload is stored under the build
fingerprint. HCP Packer instead creates a random identifier, which is a
recorded divergence. Nothing in the supported client surface parses the name
in either case.

### Reading and downloading

Prerequisites: The `reader` role and a build with an SBOM.

1. Open the build's SBOM card in the console, or use the API to list and
   download its SBOMs.

Downloads always return the decompressed document as `<name>.json`,
regardless of the format used at upload.

## The package inventory

Each SBOM is parsed during upload in the transaction that stores it. The
build packages response contains one row per `(name, version, purl)` and
identifies the SBOMs that supplied the row. The console shows the same
inventory on the build screen's **Packages** tab.

A document that cannot be parsed remains stored with an explicit unparseable
status. The packages response reports that status instead of returning an
empty list that could be mistaken for an SBOM with no packages.

::: info
Package names, versions, and purls are reported by the client. Dufflebag
records them; it does not verify the SBOM against the image.
:::

## Vulnerability findings

Scanning is optional — the operator side (enabling the adapter, what leaves
the deployment, transcripts) is
[Vulnerability scanning](../components/vulnerability-scanning.md).

::: info
No scan is represented as absent, not as an empty result. An unscanned build
has no `vuln_details`. Bucket-level vulnerability reads return not-found.
The console does not describe an unscanned state as clean.
:::

Only a current successful scan produces findings, including an empty
findings list when the scan found nothing. A newer failed scan does not
erase the findings from the previous successful run.

![Dufflebag build packages tab showing vulnerability findings](/screenshots/scanner-findings.png)

With a scanner configured:

- Build packages include their findings. Responses include
  `Dufflebag-Scan-*` headers with the adapter, engine, database revision,
  and coverage counts.
- Bucket-level `reader` operations aggregate across builds. They return a
  vulnerability summary, packages with vulnerabilities, and a flat
  vulnerability list.
- Console package tables show severity counts. Rows with findings expand to
  show individual findings. Rows without findings do not expand.

When findings require removing an image from circulation, revoke the
version: channels roll back, and consumers stop resolving it. See
[Versions — revoking](./versions.md#revoking-a-version).
