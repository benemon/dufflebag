# MCP server

`dufflebag-mcp` exposes a Dufflebag instance to agentic clients as a set of
typed tools over the [Model Context Protocol](https://modelcontextprotocol.io/).
A client that speaks MCP can query registry state, check whether an artifact
is safe to consume, drill into vulnerability findings, generate ready-to-paste
consumption configuration, and — with the right authority — promote a version
to a channel.

The server is an ordinary API client. Registry reads go through the
compatibility plane, tenancy and operational state through the platform
plane, and nothing it does carries authority the supplied credential does not
already hold. It lives in its own repository:
[benemon/dufflebag-mcp](https://github.com/benemon/dufflebag-mcp).

::: info
The MCP server is experimental. Its tool surface may change between releases.
:::

## How it works

The transport is MCP stdio: the client spawns the server process and speaks
newline-delimited JSON-RPC over stdin and stdout. The server holds a service
principal's client id and secret, mints access tokens through the instance's
ordinary token endpoint, and refreshes them as they expire. There is no new
authentication surface — revoking the service principal's secret cuts the
server off like any other API client.

Every tool that operates on registry state takes `organization_id` and
`project_id` arguments, and each falls back to the configured defaults, so a
server registered against one project needs no tenancy boilerplate on any
call.

## Requirements

- A serving Dufflebag instance and its base URL.
- A [service principal](../administration/principals.md) with a role matching
  the tools you intend to use: `reader` covers every read, `publisher` adds
  channel promotion, and organization or project creation needs platform
  authority. Prefer the least privilege that covers the intended use.
- The CA chain, as a PEM file, when the instance serves a certificate from a
  private CA.

## Run the server

As a binary:

```sh
git clone https://github.com/benemon/dufflebag-mcp.git
cd dufflebag-mcp
make build
```

As a container, using the published image (same UBI micro base as the
registry image):

```sh
docker run -i --rm \
  -e DFBG_MCP_ENDPOINT=https://dufflebag.example.com:8443 \
  -e DFBG_MCP_CLIENT_ID=... \
  -e DFBG_MCP_CLIENT_SECRET=... \
  quay.io/benjamin_holmes/dufflebag-mcp:latest
```

The transport is stdio, so the container must run with an attached stdin
(`-i`). A private CA chain mounts in as a file, named by `DFBG_MCP_CA_FILE`.

## Configure an MCP client

Most MCP clients accept a server entry naming a command and its environment.
The shape varies by client; the common JSON form is:

```json
{
  "mcpServers": {
    "dufflebag": {
      "command": "/path/to/dufflebag-mcp",
      "env": {
        "DFBG_MCP_ENDPOINT": "https://dufflebag.example.com:8443",
        "DFBG_MCP_CLIENT_ID": "...",
        "DFBG_MCP_CLIENT_SECRET": "...",
        "DFBG_MCP_CA_FILE": "/path/to/ca-chain.pem",
        "DFBG_MCP_ORGANIZATION_ID": "...",
        "DFBG_MCP_PROJECT_ID": "..."
      }
    }
  }
}
```

The credential sits in client configuration, so treat that file as a secret
store: scope the service principal down rather than reaching for a root or
maintainer credential, and prefer the read-only posture below for clients
that only consume.

## Configuration reference

| Variable | Description | Required |
| --- | --- | --- |
| `DFBG_MCP_ENDPOINT` | Base URL of the instance | yes |
| `DFBG_MCP_CLIENT_ID` | Service principal client id | yes |
| `DFBG_MCP_CLIENT_SECRET` | Service principal client secret | yes |
| `DFBG_MCP_CA_FILE` | PEM chain for a private CA | no |
| `DFBG_MCP_ORGANIZATION_ID` | Default organization for tenancy-scoped tools | no |
| `DFBG_MCP_PROJECT_ID` | Default project for tenancy-scoped tools | no |
| `DFBG_MCP_BUCKET_ID` | Default bucket id for bucket-taking tools; bucket-scoped credentials need none | no |
| `DFBG_MCP_READ_ONLY` | When truthy, mutating tools are neither listed nor callable | no |

A bucket-scoped service principal uses the ordinary client id and secret
variables — the scope rides in the credential, and the server confines it
regardless of client configuration. From dufflebag-mcp v0.2.1 it needs no
`DFBG_MCP_BUCKET_ID`: the server reads the credential's own bucket binding
from the registry, `whoami` reports it as `bucket_id`, and bucket-taking
tools omit their bucket argument.

`DFBG_MCP_BUCKET_ID` remains for wider credentials pinning a default bucket —
and for bucket-scoped principals on older dufflebag-mcp releases. Set it to
the bucket's ULID (its id, not its name); the first bucket-taking call
resolves the id against the buckets the credential can see, and answers with
the visible bucket ids when the declared one is not among them. The console
prints the ULID in the ready-to-paste MCP environment block shown when a
bucket-scoped credential is issued — at creation, or from **Issue secret** on
an existing principal — and nowhere else; if the block is gone, issue a fresh
secret rather than hunting for the id.

## Available tools

### Identity and tenancy

| Tool | Purpose | What it returns |
| --- | --- | --- |
| `whoami` | Identify the credential in use | Principal, role, bound scope, resolved tenancy defaults, read-only posture |
| `list_organizations` | Organizations visible to the credential | Id, name and creation time per organization |
| `create_organization` | Create an organization (mutating) | The new organization |
| `list_projects` | An organization's projects | Id, name and creation time per project |
| `create_project` | Create a project (mutating) | The new project |

Example prompt: *"What credential is the registry connection using, and which
project is it pointed at?"*

### Registry reads

| Tool | Purpose | What it returns |
| --- | --- | --- |
| `list_buckets` | A project's buckets | Compact rows: name, platforms, the latest version's identity and revocation flag, and ancestry freshness — build detail stays with `list_versions` |
| `list_versions` | A bucket's versions, newest first | Versions with builds, artifacts and revocation state |
| `list_channels` | A bucket's channels | Channels with their assigned versions |
| `resolve_channel` | Resolve a channel before consuming | The assigned version, its fingerprint, and whether it is safe to consume |
| `version_diff` | What changed between two versions | Builds added, removed and changed, and each side's revocation state |
| `check_ancestry` | Parent/child lineage freshness | Each relation's status, including what the parent channel now serves when the child is out of date |
| `find_artifact` | Provenance in reverse: which version produced an artifact | Matching bucket, version and build for an image digest or machine image id, served by the registry's search endpoint; against registries that predate it the tool enumerates the project's buckets and says so |

Example prompt: *"Which version does the release channel of demo-ubuntu
serve, is it safe to consume, and what changed since the version before it?"*

### Security

| Tool | Purpose | What it returns |
| --- | --- | --- |
| `vulnerability_summary` | Headline scanner counts for a bucket | Totals by criticality, then one aggregated row per package sorted worst-first, capped with an explicit omission count pointing at `list_vulnerabilities` |
| `list_vulnerabilities` | Individual findings, filtered | Identifier, criticality, impacted packages and channels, and the fixed version where one exists |

Example prompt: *"List the critical findings on demo-ubuntu that have a fix
available."*

### Consumption

| Tool | Purpose | What it returns |
| --- | --- | --- |
| `consume_snippet` | Ready-to-paste consumption for a version | Terraform `hcp_packer_version`/`hcp_packer_artifact` data sources for any platform; a docker or AWS form when that platform was built |

Example prompt: *"Give me the Terraform to consume the release channel of
demo-ubuntu."*

### Mirroring

| Tool | Purpose | What it returns |
| --- | --- | --- |
| `bagdrop_status` | [Bag Drop](../administration/bag-drop.md) mirror state for the project | Configuration, cadence and per-bucket sync state |

### Publishing

| Tool | Purpose | What it returns |
| --- | --- | --- |
| `promote_channel` | Assign a version to a channel (mutating) | The updated channel and its assignment |

`promote_channel` requires the exact version fingerprint — there is no
promote-whatever-is-on-latest shortcut — so the version a client verified
with `version_diff`, `vulnerability_summary` and `resolve_channel` is the
version that lands on the channel. `create_if_missing` creates the channel
with the assignment when it does not yet exist. Managed channels are refused,
matching the API.

Example prompt: *"Diff v3 against what release currently serves, check the
scanner findings, and if both are clean promote v3 to release."*

## Read-only posture

Setting `DFBG_MCP_READ_ONLY` removes `create_organization`, `create_project`
and `promote_channel` from the advertised tool list and refuses their
dispatch. Combined with a `reader` service principal this gives a deployment
that cannot mutate the registry regardless of what a client asks for —
`whoami` reports the posture, so a client can tell why the writes are
absent.
