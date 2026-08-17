# HCP Packer API compatibility reference

The contract dufflebag serves, established by reading upstream client
source — not inferred from documentation — with a source link per section so
findings can be re-checked when upstream moves. Findings marked with a probe
date were verified against the live HCP Packer service; the numbered probe
transcripts those citations refer to are retained by the maintainers outside
this repository. This document is the authoritative statement of the
contract: where it and published documentation disagree, observed client
behaviour wins.

Upstream revisions read: `hashicorp/packer@main`, `hashicorp/hcp-sdk-go@main`.

ADRs cited by number throughout (ADR-0001, ADR-0013, …) are the project's
architecture decision records, retained by the maintainers outside this
repository — like the probe transcripts, they are provenance for a claim, not
required reading for using the document.

---

## Client redirection

No `/etc/hosts` or DNS interception is needed — the stock binary honours the
SDK's environment surface. Call chain:

```
packer.NewClient()                        internal/hcp/api/client.go
  └─ httpclient.New(httpclient.Config{})  hcp-sdk-go/httpclient/httpclient.go
       └─ cfg.Canonicalize()
            └─ config.NewHCPConfig(config.FromEnv())
```

`Canonicalize()` applies `config.FromEnv()` unconditionally when no explicit
`HCPConfig` is supplied — and Packer supplies only `SourceChannel`. So the full
environment surface in `hcp-sdk-go/config/env.go` is live in the stock binary.

### Recognised environment variables

| Variable | Effect |
|---|---|
| `HCP_API_ADDRESS` | API address as `hostname[:port]`, **no scheme** |
| `HCP_API_HOST` | Legacy alias; strips a leading `https://` |
| `HCP_AUTH_URL` | Auth base URL; also rewrites the OAuth2 auth/token endpoints |
| `HCP_OAUTH_CLIENT_ID` | OAuth2 client ID for browser login |
| `HCP_CLIENT_ID` / `HCP_CLIENT_SECRET` | Service-principal client credentials |
| `HCP_ORGANIZATION_ID` / `HCP_PROJECT_ID` | Profile; **only applied when both are set** |
| `HCP_API_TLS` | `insecure` (skip verify) or `disabled` (plain HTTP) |
| `HCP_AUTH_TLS` | `insecure`, or `disabled` — the latter is accepted without error but does **not** disable anything (see correction below) |
| `HCP_PORTAL_URL`, `HCP_SCADA_ADDRESS`, `HCP_SCADA_TLS` | Must be non-empty; geography defaults suffice |
| `HCP_GEOGRAPHY` | Applied *first* in `FromEnv`, so individual vars override it |

### Constraints enforced by `hcpConfig.validate()`

- The auth URL **must** use scheme `https`. There is no override. However
  `HCP_AUTH_TLS=insecure` is read *after* `WithAuth()` in `FromEnv`, so it wins
  and a self-signed auth cert works.
- `apiAddress` must be non-empty — that is the only check `validate()` makes on
  it; a scheme-bearing value passes validation and fails later, at the HTTP
  layer. `HCP_API_TLS=disabled` sets `apiTLSConfig = nil`, which makes
  `httpclient.New` select scheme `http`.
- `portalURL` and `scadaAddress` must be non-empty — satisfied by the default
  US geography config without any action.
- Either client credentials or an OAuth2 client ID must be present.

> **Correction (2026-08-11):** two claims above previously overstated the SDK
> (review finding 1.13, verified against `hcp-sdk-go@v0.174.0 config/`). The
> variable table said `HCP_AUTH_TLS` accepts `insecure` only; in fact
> `tlsConfigForSetting` (`config/env.go:180-190`) parses `disabled` without
> error and sets `authTLSConfig = nil`, which means *default* certificate
> verification — the claimed effect (auth TLS cannot be disabled) is right,
> but the setting is silently accepted and un-does a previously set
> `insecure`. The constraints list said `apiAddress` must "carry no scheme";
> `validate()` (`config/hcp.go`) checks non-empty only, and no scheme check
> exists anywhere in `config/`.

With a private CA available, issue real certificates and skip the insecure
flags entirely.

Source: [config/env.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/config/env.go),
[config/hcp.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/config/hcp.go),
[config/with.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/config/with.go),
[httpclient/httpclient.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/httpclient/httpclient.go)

---

## Tenancy pinning and discovery

`NewClient()` returns early when the profile has both IDs:

```go
if hcpClientCfg.Profile().OrganizationID != "" && hcpClientCfg.Profile().ProjectID != "" {
    client.OrganizationID = hcpClientCfg.Profile().OrganizationID
    client.ProjectID = hcpClientCfg.Profile().ProjectID
    return client, nil
}
```

Setting both `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` therefore skips:

- `OrganizationService_List` (`cloud-resource-manager/2019-12-10`)
- `ProjectService_List` (same)
- `ValidateRegistryForProject` → `PackerService_GetRegistry`

This is the single largest scope reduction available, and it remains true — but
per ADR-0003 dufflebag implements the resource-manager
endpoints anyway, so that all four resolution paths work as they do against real
HCP. Pinning is an optimisation, not a requirement.

`profile.UserProfile` performs **no UUID validation**, so the IDs can be any
non-empty strings — though real UUIDs are advisable for realism and for the
Terraform provider.

Source: [internal/hcp/api/client.go](https://github.com/hashicorp/packer/blob/main/internal/hcp/api/client.go),
[profile/profile.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/profile/profile.go)

> **Correction, verified 2026-08-01 against terraform-provider-hcp v0.112.0:**
> the early return above describes Packer, not the Terraform provider. The
> provider reads `HCP_PROJECT_ID` when its explicit `project_id` is unset and,
> when either supplies a value, calls `ProjectService_Get` before configuring
> any resource or data source. It uses the returned project's `parent.id` as
> the organization. Therefore a pinned provider still reaches
> `GET /resource-manager/2019-12-10/projects/{id}`; only Packer makes no
> resource-manager calls with both ids pinned.
> Source: [terraform-provider-hcp v0.112.0 `internal/provider/provider.go`, lines 291-306](https://github.com/hashicorp/terraform-provider-hcp/blob/v0.112.0/internal/provider/provider.go#L291-L306)
> calling [`internal/clients/retry_request.go`, lines 88-97](https://github.com/hashicorp/terraform-provider-hcp/blob/v0.112.0/internal/clients/retry_request.go#L88-L97)
> (the muxed SDKv2 provider has the same branch at
> [`internal/providersdkv2/provider.go`, lines 181-196](https://github.com/hashicorp/terraform-provider-hcp/blob/v0.112.0/internal/providersdkv2/provider.go#L181-L196)).

---

## Authentication

`clientcredentials.Config` posts to `{authURL}/oauth2/token` with:

- a standard client-credentials grant, with the **client id and secret in an
  `Authorization: Basic` header, not form fields** — `clientcredentials.Config`
  uses `AuthStyleInHeader`. The spike observed the first token request logging
  `client_id=` empty for exactly this reason. Accept both.
- an extra parameter `audience=https://api.hashicorp.cloud` (constant
  `APIAudienceID`, identical across all environments)

Expected response is a standard OAuth2 token document:

```json
{"access_token": "...", "token_type": "Bearer", "expires_in": 3600}
```

**Probe 2026-07-31:** confirmed live. `POST https://auth.idp.hashicorp.com/oauth2/token`
with HTTP Basic client credentials and `audience=https://api.hashicorp.cloud`
returned 200 with exactly those three fields — no scope, no refresh token.

The client performs **no validation of the token's contents** — it is attached
as a bearer token by `oauth2.Transport` and nothing more, so an opaque random
string would satisfy Packer.

> ADR-0004 nonetheless decided on a **signed JWT with
> claims**: the token is the contract, not the credential, so every consumer
> authorizes off verified claims and a second issuer (workload-identity
> federation) can be added without touching them. What Packer will *accept* and
> what dufflebag chooses to *issue* are different questions.

### Token endpoint responses

Beyond the 200 token document, the endpoint has five other wire-visible
outcomes, all carried in a standard OAuth `{"error", "error_description"}`
body — **not** `google.rpc.Status`:

- `400 unsupported_grant_type` for any grant other than `client_credentials`;
- `400 invalid_request` for an `audience` that is present but not
  `https://api.hashicorp.cloud`;
- `401 invalid_client` with `WWW-Authenticate: Basic realm="dufflebag"` for
  an unknown client id and a bad secret alike — indistinguishable by design;
- `429` with `Retry-After: 1` from the per-caller admission bucket;
- `503 temporarily_unavailable` when the argon2id verification-memory budget
  is exhausted.

A client's retry logic must tolerate the last two: they are admission
control, not authentication verdicts.

### The token cache

`tokencache.NewServicePrincipalTokenSource` persists tokens to
`~/.config/hcp/creds-cache.json` (note the spelling — `creds-cache`, not
`cred_cache`; the constant is `files.TokenCacheFileName`), keyed by
**client ID + geography**. Reusing a
client ID that has also been used against real HCP will collide and produce
confusing 401s. Always use a distinct `HCP_CLIENT_ID` for this registry.

### Credential files

`env.HasHCPAuth()` also accepts a credential file at `~/.config/hcp/cred_file.json`
or `$HCP_CRED_FILE`, supporting both a service-principal scheme and a workload
identity scheme. Client credentials via environment are simpler.

The workload-identity scheme is not merely "later": it reaches
`POST {API}/2019-12-10/{wip-resource-name}/exchange-token`, which is in the
compatibility surface per ADR-0001 and *is* the
OIDC federation feature per ADR-0004. Its wire contract
is fixed by HCP, not dufflebag's to design.

Source: [config/tokensource.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/config/tokensource.go),
[internal/hcp/env/env.go](https://github.com/hashicorp/packer/blob/main/internal/hcp/env/env.go)

---

## API surface

Base path: `/packer/2023-01-01/organizations/{location.organization_id}/projects/{location.project_id}`

The generated client exposes **52 operations**. Thirteen are on the path Packer
actually exercises during a build.

### The build path

| Operation | Method | Path suffix |
|---|---|---|
| `GetBucket` | `GET` | `/buckets/{bucket_name}` |
| `CreateBucket` | `PUT` | `/buckets` |
| `UpdateBucket` | `PATCH` | `/buckets/{bucket_name}` |
| `GetVersion` | `GET` | `/buckets/{bucket_name}/versions/{fingerprint}` |
| `CreateVersion` | `POST` | `/buckets/{bucket_name}/versions` |
| `CreateBuild` | `POST` | `/buckets/{bucket_name}/versions/{fingerprint}/builds` |
| `ListBuilds` | `GET` | `/buckets/{bucket_name}/versions/{fingerprint}/builds` |
| `UpdateBuild` | `PATCH` | `/buckets/{bucket_name}/versions/{fingerprint}/builds/{build_id}` |
| `GetChannel` | `GET` | `/buckets/{bucket_name}/channels/{channel_name}` |
| `UpdateChannel` | `PATCH` | `/buckets/{bucket_name}/channels/{channel_name}` |
| `UploadSbom` | `PUT` | `.../builds/{build_id}/sboms` |
| `GetEnforcedBlocksByBucket` | `GET` | `/enforced_blocks/bucket/{bucket_name}` |
| `GetRegistry` | `GET` | `/registry` |

> Earlier revisions listed `CreateBucket (upsert)` as one operation and counted
> twelve. The spike disproved it: `UpsertBucket` is `GetBucket` → on code 5
> `CreateBucket`, otherwise compare metadata and `UpdateBucket` on mismatch —
> three endpoints. [Build lifecycle](#build-lifecycle) has the observed order.

`GetRegistry` is reached in exactly one of the four org/project resolution paths
(project pinned, org not) and must return a non-nil registry there — see
ADR-0003. It is **not** among the twelve operations
`terraform-provider-hcp` reaches; ADR-0013
enumerates those.

### The read surface

> The original text here listed the entire read surface as deferrable, to be
> `501`'d until something demanded it.
> ADR-0006 overturned that: the UI is a
> compatibility-plane client, and these endpoints **are** the UI.
> `ListBuckets`, `ListVersions`, `ListChannels`, `ListChannelAssignmentHistory`
> and `ListBucketAncestry` are core, not optional.

**Implementation note, verified 2026-08-03.** Ancestry persists Packer's
terminal `UpdateBuild` `parent_version_id` and `parent_channel_id`, joins those
identifiers across buckets inside the authorized tenant, and compares the
recorded parent version with the channel's current assignment for
`UP_TO_DATE` / `OUT_OF_DATE`; a missing or unassigned channel is
`UNDETERMINED`. [Build ancestry](#build-ancestry) records the exact client condition and why a live
HCP response containing null parent fields does not establish that Packer
omitted them from its request.

Still genuinely deferrable: enforced-block CRUD and all `runtasks`
endpoints. The `packages` and `vulnerabilities` operations, listed here as
deferrable in earlier revisions, are now served — see
[Scope boundaries](#scope-boundaries). External artifact search, deferrable
in earlier revisions, is served as of 2026-08-16 — see the table below.

### Served beyond the build path

The residual "everything else is deferrable" framing undersells the served
surface. Beyond the build path and the list operations above, these are
routed and tested:

| Operation | Method | Path suffix | Authority |
|---|---|---|---|
| `CreateChannel` | `POST` | `/buckets/{bucket_name}/channels` | publisher |
| `AssignChannelVersion` | `POST` | `/buckets/{bucket_name}/channels/assign` | publisher |
| `DeleteChannel` | `DELETE` | `/buckets/{bucket_name}/channels/{channel_name}` | publisher |
| `DeleteBucket` | `DELETE` | `/buckets/{bucket_name}` | publisher |
| `UpdateVersion` (revocation and restore) | `PATCH` | `/buckets/{bucket_name}/versions/{fingerprint}` | publisher |
| `DeleteVersion` | `DELETE` | `/buckets/{bucket_name}/versions/{fingerprint}` | publisher |
| `DeleteBuild` | `DELETE` | `.../builds/{build_id}` | publisher |
| `ListSboms` | `GET` | `.../builds/{build_id}/sboms` | reader |
| `GetSbom` | `GET` | `.../builds/{build_id}/sboms/{sbom_name}` | reader |
| `DownloadSbom` | `GET` | `.../builds/{build_id}/sboms/{sbom_name}/download` | reader |
| `ListBuildPackages` | `GET` | `.../builds/{build_id}/packages` | reader |
| `ListBucketPackagesVulnerabilitySummary` | `GET` | `/buckets/{bucket_name}/packages/vulnerability-summary` | reader |
| `ListBucketPackagesWithVulnerabilities` | `GET` | `/buckets/{bucket_name}/packages/with-vulnerabilities` | reader |
| `ListBucketVulnerabilities` | `GET` | `/buckets/{bucket_name}/vulnerabilities` | reader |
| `SearchExternalArtifact` | `POST` | `/_search/external_artifact` | reader |

**External artifact search, served 2026-08-16.**
`PackerService_SearchExternalArtifact` answers provenance in reverse: which
bucket, version and build produced a given external identifier (an image
digest, a machine image id). `external_identifier` is required; `platform`
and `region` are optional exact filters. Matches span buckets within the
authorized tenant, revoked versions are returned with their revocation state
— the caller's question is whether the artifact is still vouched for — and
results order newest version first with the shared offset pagination. No
stock client calls this operation; it exists for external consumers asking
the incident-response question.

**Version revocation, served 2026-08-08.** `PackerService_UpdateVersion`
(`PATCH …/versions/{fingerprint}`) is served for its revocation capability:
`revoke_at` / `revoke_in` (exactly one; `d` unit expanded to hours),
`revocation_message`, and `skip_descendants_revocation`. Revocation takes
effect at `revoke_at` — status is derived per read as `VERSION_REVOKED` once
the time passes and `VERSION_REVOCATION_SCHEDULED` before it, matching the
deprecated data sources' `revoke_at < now` rule; the modern data sources
refuse builds only on `VERSION_REVOKED`
(`datasource/hcp-packer-version/data.go:129`). Descendants inherit
transitively down the recorded build ancestry with
`revocation_type: INHERITED` and a `revocation_inherited_from` naming the
revoked ancestor; a descendant already revoked keeps its own record. Neither
Packer nor terraform-provider-hcp ever calls `UpdateVersion`, so the write
side carries no bug-for-bug pressure. `complete` remains refused with code 3
rather than silently ignored because completion is derived from builds here.

**Record-time inheritance, served 2026-08-12 (probe A.14).** When a stored
build first records an explicit `parent_version_id` whose in-tenant parent is
already revoked or scheduled for revocation, the child version immediately
receives the same effect time, message, and author as an inherited revocation
naming that parent. This runs on both `CreateBuild` and `UpdateBuild`, inside
the build transaction, and deliberately does not infer edges from
`source_external_identifier`. A child that already carries any revocation
keeps it. Completion still assigns a scheduled-future child to managed
`latest`, but does not assign a child whose inherited revocation is already
effective.

**Version restore, served 2026-08-11.** `restore: true` clears a `REVOKED` or
`REVOCATION_SCHEDULED` version and, in the same transaction, every descendant
whose inherited revocation names that version. Manual descendant revocations
and revocations inherited from another ancestor stand. Restoring an active
version returns code 9 with the live message `Restoring does not apply. This
version is valid and it is not scheduled to be revoked. ` (including the
trailing space); combining restore with `revoke_at` or `revoke_in` returns code
3. Restore does not forward-roll channel assignments. In the recorded diamond
limit, a descendant also covered by a second still-revoked ancestor becomes
active because inheritance is computed at revoke time only (duf-1hy6).
Record-time inheritance likewise uses first-revocation-wins, deliberately
diverging from HCP's documented but unprobed earliest-date rule for diamonds.
As live-proven on 2026-08-12 (probe A.14), directly restoring an
inherited-revoked version is refused with `Directly restoring this version does
not apply. The revocation status is inherited from an ancestor version. To
restore this version, the revoked ancestor should be restored.` Restore the
ancestor instead, which clears its inherited descendants.

Channel rollback matches upstream (duf-h37y, confirmed by a 2026-08-09 live
probe: revoking a version rolls every channel then pointing at it — user
channels and the managed `latest` alike — back to the most recent assignment
in its history whose version is not revoked, all in the revoke transaction).
`disable_rollback_channels: true` opts out and leaves the assignments in
place. A channel whose entire history is revoked has no valid target and is
left as-is rather than invented into an unassigned state.

**Version and build deletion, served 2026-08-12 (probes A.15/A.15b).**
`DeleteVersion` and `DeleteBuild` return the empty object their vendored
response definitions declare. A version currently assigned to any user
channel is refused with HTTP 400 / code 9 and `Version is assigned by channels:
&lt;name&gt;. Please, remove the channels assignment before deleting the version.`
Multiple current user-channel names are sorted ascending and joined with `, `;
that separator is Dufflebag's interpolation because the captured refusal had
one channel. The managed `latest` channel is not a blocker. Deleting its target
rolls it to the newest surviving complete, unrevoked version using the same
newest-valid assignment-history selector as revocation rollback; deleting the
last version records the fresh-bucket unassigned shape (`version: null`).
Revoked and incomplete `v0` versions are deletable.

Deletion is complete across the relational and object-store aggregate. Version
deletion removes its builds, artifacts, projected packages and SBOM rows;
build deletion removes that build's corresponding children without changing
the version's name, completion, sequence or active status, including when it
leaves zero builds. Bucket deletion now uses the same SBOM-object cleanup.
Object keys are selected inside the database transaction, relational cascades
commit first, and object deletion is best-effort afterward: a failed cleanup
can leave only a harmless orphan, never a surviving row pointing at deleted
bytes. The same version fingerprint can then be recorded afresh.

Sequence allocation remains `max(surviving sequence)+1`: deleting `v2` and
then completing a replacement therefore reuses `v2`. Misses deliberately keep
the captured endpoint asymmetry. `DELETE` of a missing version is HTTP 404 /
code 5, `Error: The version with identifier <fingerprint> does not exist.`,
while `GET` keeps its HTTP 409-ish / code 10 version-identity miss. A missing
build is HTTP 404 / code 5, `The build with identifier <build_id> does not
exist.`

### The 2021-04-30 API

> **Correction, 2026-08-01:** the earlier “required, but only two endpoints” /
> “required, and cheap” classification is withdrawn. The coverage rule is
> forward-looking: every API a **supported client version** reaches must be
> compatible. Per ADR-0013,
> Packer is supported from v1.15.4 but its deprecated `hcp-packer-image` and
> `hcp-packer-iteration` data sources are not; terraform-provider-hcp is
> supported from v0.84.0, which no longer registers the corresponding data
> sources. The 2021-04-30 plane therefore serves zero operations.

The deprecated Packer data sources use a separate `2021-04-30` client
(`internal/hcp/api/deprecated_client.go`) with the older image vocabulary. The
vendored spec and Appendix A capture remain reference evidence, but the server
does not mount that API tree. Requests fall through the `/packer/` plane's
parseable HTTP 501 / gRPC code 12 refusal.

Source: [packer_service_client.go](https://github.com/hashicorp/hcp-sdk-go/blob/main/clients/cloud-packer-service/stable/2023-01-01/client/packer_service/packer_service_client.go)

### The resource-manager API

The reachable resource-manager surface is three of its 35 operations:
`OrganizationService_List`, `ProjectService_List`, and `ProjectService_Get`.
The first two are Packer/provider discovery calls. The third is provider-only
when a project id is pinned, as corrected under [Tenancy pinning and discovery](#tenancy-pinning-and-discovery). The scope rule excludes the
other 32 operations.

---

## Validation posture

Measured across `cloud-packer-service` 2023-01-01: **zero** occurrences of
`pattern`, `maxLength` or `minLength`, and `required` appears on none of its 132
definitions. No schema keyword constrains bucket names, channel names or
fingerprints; a handful of field *descriptions* state constraints in prose,
which a generator cannot enforce.

So validation rules cannot be derived from the spec, and there are no observed
upstream rejections to copy either. That gives one asymmetry worth stating as a
rule:

> **Where the spec is silent on validation, be permissive rather than strict.**
> Rejecting a name HCP would have accepted breaks a client that works against
> real HCP. Accepting one HCP would have rejected diverges only in a direction no
> client exercises.

Concretely: require non-empty where a value is structurally necessary, and do not
invent character classes, length caps or regexes. Add a constraint when something
actually demands it, and record what demanded it.

**Probe 2026-07-31 — the unknown-field question is settled: HCP silently
ignores unknown request body fields.** `UpdateBucket` with a bogus top-level
field returned 200 and applied the known fields (Appendix A, probe 20); an
`UpdateBuild` whose entire body was wrapped in an unknown `updates` object
returned 200 as a no-op rather than 400 (probe 11). This contradicted the
server's then-current `DisallowUnknownFields` posture, which turned the same
requests into `400 {"code":3}`. **Resolved 2026-08-01**: the compat-plane
decoder no longer rejects unknown fields, matching the probed behaviour
(duf-7cy).

One counter-example where live HCP *is* strict and the spec is silent:
`UpdateChannel` **requires `update_mask`**. Omitting it returns
`400 {"code":3}` with a populated `google.rpc.BadRequest` detail — the only
observed error carrying non-empty `details` (Appendix A, probe 15). Note the
detail body contains no incidental `code` field, so Packer's regex is safe.

**Probe 2026-08-01 — request size is bounded outside the documented
application contract.** A producer-valid SPDX upload with 1,049,625 compressed
bytes (1,399,558 HTTP request bytes after base64 and JSON framing) returned 200.
The next coarse step, 5,244,663 compressed bytes / 6,992,942 request bytes,
returned bare `HTTP 504` with body `Gateway Timeout` (Appendix A, probes 66–67).
That is a gateway/transport-shaped refusal, not a `google.rpc.Status` body and
not evidence of an exact numeric threshold; the honest observed ceiling is
bracketed between those two requests. Dufflebag therefore defaults its entire
2023 compatibility plane to a 4 MiB raw request-body limit: 2.99 times the
accepted request, 40.0% below the first refusal, and enough for approximately
3 MiB of compressed SBOM bytes after encoding overhead. The limit is
configurable rather than inferred from the constraint-free spec. The refusal
itself is reproduced as observed: an over-limit body answers `504` with a
`text/plain` body of exactly `Gateway Timeout` — deliberately **not** a
`google.rpc.Status`, and therefore the one served refusal exempt from the
parseable-body rule that [version identity errors](#version-identity-errors) and the 501 catch-all otherwise insist on.

---

## Client quirks

These are behaviours the client depends on that the published API docs do not
describe. Getting any of them wrong breaks the build in a way that is hard to
diagnose from Packer's error output.

> **Empirically verified 2026-07-29** against Packer v1.16.0 by a live spike,
> and continuously since by the real-binary `make test-packer` lane. The spike
> corrected two orderings below and added four further quirks (the not-found
> code differs per resource; incomplete versions must be named `v0`;
> credentials arrive via HTTP Basic; the token cache masks auth bugs).

### Version identity errors

This is the most important quirk in the entire project.

Note the asymmetry confirmed by the spike: a missing **bucket** must answer
code **5** (`NotFound`), a missing **version** must answer code **10**
(`Aborted`). There is no general convention — match each endpoint individually.

**Probe 2026-07-31 — the HTTP statuses carrying these codes are now observed
fact** (Appendix A has the verbatim pairs):

| Condition | HTTP status | Body |
|---|---|---|
| `GetVersion`, unknown fingerprint | **409** | `{"code":10, "message":"Version with fingerprint … not found", "details":[]}` |
| `GetBucket` / `DeleteBucket`, missing bucket | **404** | `{"code":5, "message":"Error: The bucket with identifier … does not exist.", "details":[]}` |
| `GetChannel` / `UpdateChannel`, missing channel | **404** | `{"code":5, "message":"Error: The channel with identifier … does not exist.", "details":[]}` |
| `CreateBucket`, name exists | **409** | `{"code":6, "message":"Error: The bucket with identifier … already exists.", "details":[]}` |
| `CreateChannel`, name exists | **409** | `{"code":6, "message":"Error: The channel with identifier … already exists.", "details":[]}` |
| Managed-channel mutation refusals | **400** | `{"code":3}` / `{"code":9}` — see [the managed `latest` channel](#the-managed-latest-channel) |

The mapping follows the grpc-gateway convention (3→400, 5→404, 6→409, 9→400,
10→409). Dufflebag matches the observed pairing for codes 3, 5, 6 and
10 **on the resource-not-found path**. Two deliberate paths sit outside the
blanket claim: the ADR-0016 concealment refusals (denied or absent tenant,
unresolvable principal) use HTTP **404** as the carrier with the route's
not-found code — which is 10 on every version-scoped route, a 404/code-10
pairing not observed from live HCP, safe because Packer regexes only the
code — and
a missing **build** under a valid version answers 404/code-5 `build not
found` on the SBOM and package sub-resources. The one divergence this table
recorded — code **9** emitted with HTTP 409 where live HCP pairs it with
**400** — was **resolved 2026-08-01**: the managed-channel refusals now
answer 400/code-9 as captured (see the managed-channel implementation note under [Data model](#data-model)). Packer only regexes
the code, so the status was inert for it throughout. (Code 12's status was
not observed live; nothing new there.)

```go
version, err := bucket.client.GetVersion(ctx, bucket.Name, bucket.Version.Fingerprint)
if hcpPackerAPI.CheckErrorCode(err, codes.Aborted) {
    // probably means Version doesn't exist need a way to check the error
    version, err = bucket.createVersion(templateType)
}
```

And `CheckErrorCode` is a **regular expression over the error string**:

```go
var errCodeRegex = regexp.MustCompilePOSIX(`[Cc]ode"?:([0-9]+)`)
```

with an upstream comment conceding that `status.FromError` "doesn't appear to
work for all of the Cloud Packer Service response errors."

Consequences for the server:

- A `GetVersion` for an unknown fingerprint must return an error body that
  serialises to text containing `"code":10`. A plain `404` makes Packer abort
  instead of creating the version.
- The regex matches *any* field named `code` or `Code`, quoted or not, anywhere
  in the error string. Error envelopes must not carry incidental `code` fields
  (HTTP status codes, application error codes) or the match will pick up the
  wrong number.

### Version completion

`IsVersionComplete(version)` returning true makes Packer exit with
"The version associated to the fingerprint %v is complete... a new version must
be created by changing the fingerprint." Completion semantics must match HCP's
exactly, or repeat builds against an existing fingerprint break.

The mechanism is the **name**: `IsVersionComplete` is `version.Name != "v0"`, so
completion is inferred from a string field rather than from `status`. The
2021-04-30 spec shows why — it carried an explicit `complete` boolean and an
`incremental_version` integer, and 2023-01-01 collapsed both into `name`. See
[spec/vendor/PROVENANCE.md](https://github.com/benemon/dufflebag/blob/main/spec/vendor/PROVENANCE.md).

### Template type

`createVersion` refuses to proceed if the template type is
`TEMPLATE_TYPE_UNSET`. `initializeVersion` then hard-fails if an existing
version's stored template type differs from the current run's:

> "This version was initially created with a %s template. Changing from %s to %s
> is not supported"

So the server must store `HCL2` vs `JSON` per version and return it faithfully.

### Build heartbeats

`HeartbeatBuild` periodically issues `UpdateBuild` `PATCH`es against a running
build. The server must tolerate these and not treat every PATCH as a meaningful
status transition.

### Tenancy in the path

> **Correction, 2026-08-08.** An earlier revision said `withOrgAndProjectIDs`
> "injects organization and project ID headers on every request", suggesting a
> cross-check against the path parameters. Both halves were wrong.

`withOrgAndProjectIDs` is a client runtime option that fills the generated
operations' organization and project **path parameters** — it is how the
location-scoped URL is built, not a header injector. Dufflebag derives tenancy
exclusively from those path segments and authorizes them against the token's
principal scope; any org/project headers a client happens to send are ignored
entirely, and no cross-check exists or is needed.

### SBOM upload and download

`UploadSbom` sends `{CompressedSbom, Format, Name}` in the request body —
zstd-compressed bytes inline (base64 on the wire), not a presigned-URL
handshake. Three further facts, verified 2026-07-31 against v1.16.0:

- **Any error fails the build.** `doCompleteBuild` returns
  `"Failed to upload sboms …"` and hard-returns before `markBuildComplete`, so
  there is no tolerated error code — unlike channels, whose failure is soft.
  ([internal/hcp/registry/types.bucket.go](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/types.bucket.go))
- **The response payload is ignored** — the client discards everything but the
  error, so only the status matters.
  ([internal/hcp/api/service_build.go](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/api/service_build.go))
- **`Name` may arrive empty**, and defaulting it is then the *server's* job.
  The provisioner documents "If omitted, HCP Packer uses the build fingerprint
  as the file name" and passes the empty string through
  ([provisioner/hcp-sbom/provisioner.go](https://github.com/hashicorp/packer/blob/v1.16.0/provisioner/hcp-sbom/provisioner.go)) —
  but a live probe (2026-08-08) showed real HCP defaulting the stored *name*
  to a freshly minted ULID, not the fingerprint the provisioner doc promises.
  Dufflebag currently defaults to the build fingerprint per the doc reading —
  a recorded divergence, visible only in listing names and download filenames
  for unnamed uploads; nothing in the supported client surface parses the
  name.

**The serving side, probed 2026-08-08.** `GetSbom` returns exactly
`{download_url}`; dufflebag synthesises a URL onto its own authenticated
download route rather than presigning object storage, so the transfer is
audited. The download serves the **decompressed** document as
`Content-Type: application/json` with filename `<name>.json` whatever the
stored format — the zstd envelope is transport on the way in, not contract on
the way out.

Source: [internal/hcp/api/errors.go](https://github.com/hashicorp/packer/blob/main/internal/hcp/api/errors.go),
[internal/hcp/registry/types.bucket.go](https://github.com/hashicorp/packer/blob/main/internal/hcp/registry/types.bucket.go),
[internal/hcp/api/service_build.go](https://github.com/hashicorp/packer/blob/main/internal/hcp/api/service_build.go)

### Build ancestry

Packer does have a parent-pointer path, but it is narrower than the wire model
suggests. For an HCL2 build it captures the evaluated data-source outputs while
constructing the registry
([`internal/hcp/registry/hcl.go`, lines 201–213](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/hcl.go#L201-L213)).
The modern correlation indexes every `hcp-packer-artifact` output by its
external identifier and associates the artifact's version ID; it takes a
channel ID directly when the artifact was queried by channel, or recovers one
by matching the artifact version to an `hcp-packer-version` output
([`internal/hcp/registry/ds_config.go`, lines 14–128](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/ds_config.go#L14-L128)).
The version data source reports the ID of the channel it queried
([`datasource/hcp-packer-version/data.go`, lines 100–148](https://github.com/hashicorp/packer/blob/v1.16.0/datasource/hcp-packer-version/data.go#L100-L148));
the artifact data source reports its publishing version and external identifier,
while a lookup by version fingerprint intentionally has no direct channel ID
([`datasource/hcp-packer-artifact/data.go`, lines 156–232](https://github.com/hashicorp/packer/blob/v1.16.0/datasource/hcp-packer-artifact/data.go#L156-L232)).

On the terminal update, Packer independently reports the result artifact's
source external identifier. It adds parent IDs only when that source identifier
exactly matches the data-source correlation described above
([`internal/hcp/registry/types.bucket.go`, lines 339–403](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/types.bucket.go#L339-L403));
the API client serializes those three inputs into `UpdateBuild`
([`internal/hcp/api/service_build.go`, lines 55–85](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/api/service_build.go#L55-L85)).
Running-status and heartbeat updates do not carry them
([`internal/hcp/registry/types.bucket.go`, lines 261–296 and 613–654](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/types.bucket.go#L261-L296)).
Legacy JSON registry construction installs no data-source correlation
([`internal/hcp/registry/json.go`, lines 27–31](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/json.go#L27-L31)).
Deprecated HCL2 `hcp-packer-image` / `hcp-packer-iteration` data sources install
the equivalent mapping
([`internal/hcp/registry/deprecated_ds_config.go`, lines 55–130](https://github.com/hashicorp/packer/blob/v1.16.0/internal/hcp/registry/deprecated_ds_config.go#L55-L130)).
The same mechanism is materially unchanged in supported v1.15.4, v1.16.0 and
current `main`; no other Packer client path populates either parent field.

**Investigation correction, 2026-08-03.** A live HCP Docker child resolved its
parent through `hcp-packer-version` on `latest` plus `hcp-packer-artifact` by
that fingerprint. HCP rendered both parent IDs as null on the child build while
rendering the ancestry relation in both directions. The source external
identifier equalled the parent's artifact identifier. That observation does
not establish what Packer sent: the published 2023 Build response model does
not contain either parent field, although both update request bodies do
([`hcp-sdk-go` Build model](https://github.com/hashicorp/hcp-sdk-go/blob/main/clients/cloud-packer-service/stable/2023-01-01/models/hashicorp_cloud_packer20230101_build.go),
[`UpdateBuildBody` model](https://github.com/hashicorp/hcp-sdk-go/blob/main/clients/cloud-packer-service/stable/2023-01-01/models/hashicorp_cloud_packer20230101_update_build_body.go)).

The exact parent/child template was then run with stock Packer v1.16.0 against
Dufflebag. The terminal request persisted both the resolved parent version ID
and `latest` channel ID, while its source external identifier equalled the
published artifact as expected. The local-only gate reads those persisted
request fields directly and asserts ancestry from both buckets
([`e2e/packer/e2e.test.mjs`](https://github.com/benemon/dufflebag/blob/main/e2e/packer/e2e.test.mjs),
[`e2e/packer/local-child.pkr.hcl`](https://github.com/benemon/dufflebag/blob/main/e2e/packer/local-child.pkr.hcl)) —
directly rather than through the API because dufflebag faithfully reproduces
the response-model asymmetry above: its rendered Build carries
`source_external_identifier` but neither parent field.
This agrees with the client source and establishes that the documented HCL2
shape does trigger the declared-pointer path. The HCP nulls are therefore a
response/storage quirk, or another server-side treatment of a pointer that was
sent; they are not evidence that digest matching created the edge.

**Digest matching is not implemented.** `source_external_identifier` is
client-supplied. Treating it as an ancestry join would let a builder name an
identifier it never consumed and manufacture reported provenance. That may be
an acceptable compatibility trade only if a request capture or another oracle
establishes that HCP actually does it; the matching digest in this build is
also the key Packer itself uses to choose the declared IDs, so equality alone
cannot distinguish the two mechanisms. Until then Dufflebag records the
declared IDs verbatim and does not promote the source identifier from reported
input to an ancestry claim.

---

## Build lifecycle

Order **observed on the wire** during a real v1.16.0 build (2026-07-29 spike;
re-proven on every `make test-packer` run):

1. `GetBucket(name)` — on code 5 → `CreateBucket(...)`; otherwise compare
   description/labels and `UpdateBucket(...)` only on mismatch. `UpsertBucket`
   is three endpoints, not a `PUT` upsert.
2. `GetVersion(bucket, fingerprint)` — `initializeVersion()`
   - on gRPC code 10 → `CreateVersion(bucket, fingerprint, templateType)`
   - if the returned version's `Name != "v0"` → hard fail as "complete"
3. `ListBuilds(bucket, fingerprint)` — `populateVersion()`, reconciles which
   component builds already exist
4. `CreateBuild(bucket, runUUID, fingerprint, componentType, status)` per
   missing component
5. `GetEnforcedBlocksForBucket(name)` — **late**, after `CreateBuild`, not
   during bucket initialisation. Tolerates code 5 or 12.
6. `UpdateBuild(...)` → `BUILD_RUNNING`, then periodically as a heartbeat
7. `UploadSbom(...)` if the template produced one
8. `UpdateBuild(...)` → `BUILD_DONE` with artifacts, labels, platform, parent
   version/channel, metadata — `doCompleteBuild()`
9. `UpdateChannel(bucket, channel, body)` per configured channel

`UpdateBuild` body fields: `Artifacts[]`, `Labels`, `PackerRunUUID`,
`ParentChannelID`, `ParentVersionID`, `Platform`, `SourceExternalIdentifier`,
`Status`, `Metadata`.

The two parent IDs are set-once fields. An empty stored value may be populated
by the terminal `UpdateBuild`; repeating the stored value or omitting the field
is a heartbeat-safe no-op. Changing a non-empty parent version or channel ID
to a different non-empty value is refused with HTTP 400 / `google.rpc` code 6
and the captured `You cannot override a build's … if it has already been set.`
message family (probes A.9–A.11).

**Probe 2026-07-31 — what completion does server-side.** When the last build
reached `BUILD_DONE` (step 8), in the same instant (identical sub-second
`updated_at` stamps, no further client call):

- the version's `name` went `"v0"` → `"v1"` and `status` went
  `VERSION_RUNNING` → `VERSION_ACTIVE`;
- the bucket's managed **`latest` channel was auto-assigned** the completed
  version, and the assignment-history entry carries `author_id: "HCP Packer"`
  (a human-readable marker string, not a principal id — channel `author_id`
  uses it too, while versions carry the service principal's *name*, here
  `hcp-packer-spn`, not its UUID);
- the bucket's `platforms`, `latest_version` and `version_count` reflect the
  completed version.

So completion is a server-side side effect of the final `UpdateBuild`, and
step 9 (`UpdateChannel`) is only for *user-configured* channels — `latest`
must never be written by a client — see [the managed `latest` channel](#the-managed-latest-channel).

One inference the per-resource codes above do **not** license: the code-10
shape is specific to *version identity* (`GetVersion`/`UpdateVersion` on an
unknown fingerprint). A missing **build** under a valid version — the SBOM and
package sub-resources — answers 404/code-5 `build not found`, even though
those routes conceal denied tenants with code 10 (see [version identity errors](#version-identity-errors)).

---

## Data model

Small. The domain is not where the difficulty lives; the wire-protocol quirks
are.

```
registry
  └─ bucket (name, description, labels)
       ├─ version (fingerprint, template_type, complete, sequence, created_at)
       │    └─ build (component_type, status, platform, run_uuid,
       │              labels, metadata, source_external_identifier)
       │         └─ artifact (external_identifier, region, created_at)
       └─ channel (name) ──> version   [+ append-only assignment history]
```

Three things the sketch above does not show, all decided since it was written:

- **`sequence`** is a per-bucket monotonic integer assigned **at completion**,
  not creation. With `complete` it reconstitutes the two fields 2023-01-01
  collapsed into `name`.
- **Ids are ULIDs**, not UUIDs — both published specs document them as
  "Universally Unique Lexicographically Sortable Identifier (ULID)", and ids are
  wire-visible.
- **Every registry table carries `organization_id` and `project_id`**, with
  uniqueness scoped per project — bucket names are never globally unique.
  (Instance-level tables — organizations, projects, instance, the encryption
  keyring — are necessarily outside the tenant axis.) Tenant isolation on the
  registry tables is enforced by row-level security rather than by
  application predicates alone. See ADR-0003.

### The managed `latest` channel

**Probe 2026-07-31** (Appendix A, probes 04–06 and 13–19). The 2026-07-31
review listed this as could-not-assess; it is now observed fact:

- **`CreateBucket` auto-creates a channel named `latest`** in the same instant,
  with `managed: true`, `restricted: true`, `author_id: "HCP Packer"` and
  `version: null`. It appears in `ListChannels` and answers `GetChannel`
  directly — a fresh bucket is never channel-less.
- **On version completion the service assigns the completed version to
  `latest` automatically** (see [Build lifecycle](#build-lifecycle)). `version` on the channel is the full nested
  version object, builds and artifacts included.
- **Clients cannot mutate it.** `UpdateChannel latest` (with a valid
  `update_mask` and fingerprint) → `400 {"code":9, "message":"Can't update
  channel assignment on channel \"latest\". This channel is managed by HCP
  Packer", "details":[]}`. `DeleteChannel latest` → `400 {"code":3,
  "message":"Can't delete managed channel latest, it's controlled by HCP
  Packer", "details":[]}`. Note the asymmetry: update refusal is code **9**,
  delete refusal is code **3**.
- **The distinct assignment-copy operation also refuses it.** `POST
  channels/assign` with `target_channel:"latest"` → `400 {"code":9,
  "message":"Cannot assign to managed channel 'latest'", "details":[]}`
  (probe 40). This is the vendored spec's direct analogue of dufflebag's
  assign route, not an inference from `UpdateChannel`.
- **History attribution belongs to each row.** A manual `UpdateChannel`
  assignment records the calling principal (`"hcp-packer-spn"`, probe 41),
  while completion's automatic `latest` assignments record the service author
  (`"HCP Packer"`, both rows in probe 45). The channel's current `managed`
  flag is therefore not an authorship oracle.
- **`UpdateChannel` on a missing channel does not auto-create** — plain
  `404 {"code":5}`, byte-identical in shape to the `GetChannel` miss (probe
  18). That closes the review's auto-create open question: Packer's channel
  step only updates pre-existing channels; creation is `CreateChannel`.

**Consequence for dufflebag (superseded — see the implementation note
below):** at probe time dufflebag created no `latest` channel, so
`data "hcp_packer_version" { channel_name = "latest" }` — the commonest
consumption pattern — 404'd against it where it succeeded against HCP. The
required behaviour was: auto-create a managed `latest` per bucket, auto-assign
at completion, and refuse mutation with the shapes above.

> **2026-07-31 — implemented (duf-08q):** dufflebag now auto-creates the managed `latest` at CreateBucket, auto-assigns at completion in the same transaction, and refuses mutation with the captured 400/code-9 and 400/code-3 shapes — with one deliberate deviation: the `author_id` and the refusal prose say "Dufflebag" where live says "HCP Packer", because a HashiCorp trademark must not appear in content this server originates. The 0.1.0 baseline starts with this shape; pre-0.1.0 databases are rebuildable rather than upgraded.
> The deviation is verified inert: Packer matches errors solely by numeric code (`errCodeRegex`/`CheckErrorCode`, `packer@v1.16.0 internal/hcp/api/errors.go`), and the provider keys its managed-channel handling off the `managed` boolean and typed numeric `payload.Code`, never the message or author text (`terraform-provider-hcp internal/providersdkv2/resource_packer_channel.go` create/delete paths; `resource_packer_channel_assignment.go` `channel.Managed` checks).
>
> **2026-08-01 — completeness extension (duf-why, duf-wct, duf-6i3):**
> duplicate channel creation now follows probe 38's 409/code-6 adoption shape;
> both handler and repository refuse assign-copy into managed channels with
> probe 40's 400/code-9 shape; the baseline persists assignment author per
> history row. New manual rows carry the caller principal, automatic rows carry
> `Dufflebag` under the same branding rule, and pre-migration rows carry the
> honest explicit unknown `""` when imported history has no actor.

### Restricted channels

Dufflebag enforces HCP's documented secure-channel-access semantics, calibrated
to its own role ladder. Every restricted channel, including the managed
`latest`, is omitted from lists and cannot be resolved below `builder`;
`builder` and above may consume it. Creating or managing a restricted user
channel requires `maintainer`, and an update mask naming `restricted` requires
`maintainer` in either direction. Unrestricted channel management retains its
`publisher` minimum. The existing managed-channel mutation refusals for
`latest` remain unchanged for every role.

HCP's unauthorized-consumption wire response was live-probed on 2026-08-15
with a viewer-level service principal: HCP answers a viewer's resolution of a
restricted channel with `403`/`code: 7` and a message that names the channel
and its restriction — it discloses that the channel exists. Listing behaviour
matches Dufflebag's: the restricted channel is filtered out. On the
resolution path Dufflebag **deliberately diverges**: ADR-0017's disclosure
rule holds, so resolving a restricted channel below `builder` returns the
route's byte-identical not-found/code-5 form rather than HCP's disclosing
403. Probe limits, recorded honestly: the managed `latest` was unassigned
when probed, and only the managed restricted channel was reachable — the
probing principal could not create a custom restricted channel, which itself
corroborates the maintainer-tier management rule. A caller that can see a
restricted channel but lacks `maintainer` receives the compatibility plane's
established insufficient-role 403/code-7 response.

---

## Scope boundaries

| Feature | Why |
|---|---|
| TFC run tasks | Requires HCP Terraform to call **inbound** to an HMAC-verified webhook — a different integration direction, backing a paid feature. ADR-0013 |

### Package projection from client-reported SBOMs

The earlier decision that SBOM analysis and package inventory were out of
scope is deliberately superseded. Package projection is now in scope so the
published build reads can be served:

- `GET .../builds/{build_id}/sboms` returns each stored `{id,name,format}`;
- `GET .../builds/{build_id}/packages` returns a paginated flat package list,
  with one row per `(name, version, purl)` and an `sboms[]` source array.

New uploads are decompressed and parsed during the upload, with the projection
and `parse_status` committed in the same transaction as the SBOM row; the
original zstd bytes are stored verbatim in the object store (sealed at rest on
encrypted deployments). SPDX and CycloneDX JSON are both supported. Licences
are retained in storage even though neither HCP's build-package response nor
dufflebag's exposes them. `vuln_details` is absent, not an
empty array, until that build has a current successful scan. Once a current
scan exists it is populated, including an empty findings list when the scan
really found nothing.

SPDX packages targeted by the document's `DESCRIBES` relationship are omitted
as artefact self-entries. This rule is backed by the real Packer/Syft document
generated and uploaded by `make test-packer`, whose gate computes the expected
package set from the uploaded document itself and asserts deep equality with
the served response — the exact counts vary per Syft run and are asserted, not
recorded here. CycloneDX
`components` are recursively flattened for HCP's flat response, but every
component's bom-ref/name ancestry path is stored with the row so flattening
does not discard the source format's containment information.

The 0.1.0 baseline stores only the object key and has no legacy Postgres blob
column; pre-0.1.0 databases are rebuildable rather than upgraded. A row with
`parse_status = pending` is projected lazily on its first packages read. A
corrupt or structurally unrecognised document likewise remains stored with
`parse_status = unparseable`; the packages read returns an explicit
failed-precondition response naming the SBOM instead of the indistinguishable
and false `packages: []`.

The trust boundary does not change. Package names, versions, purls and licences
are **client-reported**, not verified against the image. This matches the audit
trail's `forwarded_for` wording (“unverified forwarded-for value”, ADR-0020)
and ancestry's refusal to promote a client-supplied source digest into a
provenance claim (see [Build ancestry](#build-ancestry)): Dufflebag records and attributes the assertion without
certifying it.

### Vulnerability reads from stored scan findings

The earlier decision that CVE and vulnerability endpoints were out of scope is
deliberately superseded. The external scanner pipeline now stores attributed
findings, so the existing frozen 2023-01-01 shapes can be populated without
extending their JSON contract. These three published operations are in scope:

- `PackerService_ListBucketPackagesVulnerabilitySummary`
  (`GET .../buckets/{bucket_name}/packages/vulnerability-summary`);
- `PackerService_ListBucketPackagesWithVulnerabilities`
  (`GET .../buckets/{bucket_name}/packages/with-vulnerabilities`);
- `PackerService_ListBucketVulnerabilities`
  (`GET .../buckets/{bucket_name}/vulnerabilities`).

`PackerService_ListBuildPackages` also populates the frozen package model's
`vuln_details` from the build's current findings run. A failed newer attempt
does not erase those findings: reads follow `current_findings_run_id`, never
`latest_attempt_run_id`.

Absent-not-empty remains the compatibility rule. On a deployment with no
scanner configured, build-package `vuln_details` and every
`Dufflebag-Scan-*` attribution header are absent. The three vulnerability-only
operations retain a not-found response instead of returning a successful empty
collection; an empty success would falsely claim that an unscanned bucket is
clean. Once a build has a current successful scan, its build-package response
carries `Dufflebag-Scan-Adapter`, `Dufflebag-Scan-Engine`,
`Dufflebag-Scan-Database-Revision`, `Dufflebag-Scan-Observed-At`,
`Dufflebag-Scan-Submitted`, `Dufflebag-Scan-Invalid`,
`Dufflebag-Scan-Unversioned`, and `Dufflebag-Scan-Unsupported`. Attribution is
kept in headers because the frozen vulnerability JSON has no coverage or
provenance fields.

Findings are deduplicated by
`(build_id, package_name, package_version, purl, vulnerability_id)` before
package rows, impacts, and counts are produced. Package identity remains the
client-reported SBOM projection described above; vulnerability metadata and
severity remain the scanner's stored assertion.

> An earlier revision listed "Web UI — no API-compatibility value" here. That is
> **wrong** and was overturned: ADR-0006,
> ADR-0012 and
> ADR-0014 put a UI in scope — a
> read-only browse MVP plus first-run bootstrap and service principal
> management. It has no *compatibility* value, which is a different claim from
> having no value.

---

## The Terraform provider

**Verified.** `terraform-provider-hcp/internal/clients/client.go:141` builds its
configuration from `hcpConfig.FromEnv()` and passes it to `sdk.New`, so every
`HCP_*` override redirects it exactly as it does Packer.

Terraform is a **second client**, and the coverage rule extends to it — see
ADR-0013 for the
enumerated twelve operations it reaches and the explicit list of supported
resources. It ships **resources**, not merely data sources, which is why it is
the management interface rather than a consumer:

- resources — `hcp_packer_bucket`, `hcp_packer_channel`,
  `hcp_packer_channel_assignment`; IAM bindings supported in principle
- data sources in provider v0.112.0 — `hcp_packer_version`,
  `hcp_packer_artifact`, `hcp_packer_bucket_names`
- **not supported** — `hcp_packer_run_task`

The supported-client floors are Packer **v1.15.4 or newer** and
terraform-provider-hcp **v0.84.0 or newer** (tested provider pin: v0.112.0).
Packer's deprecated `hcp-packer-image` and `hcp-packer-iteration` data sources
are explicitly unsupported; use `hcp-packer-version` and
`hcp-packer-artifact` instead.

> **Correction, verified 2026-08-01:** `hcp_packer_image` and
> `hcp_packer_iteration` no longer exist in provider v0.112.0. They were
> registered through v0.83.0 and removed in v0.84.0. The v0.84.0 minimum now
> excludes that surface; the v0.112.0 e2e lane cannot name those data sources
> at schema validation time and no older-provider lane is run.
> Sources: [v0.83.0 registrations](https://github.com/hashicorp/terraform-provider-hcp/blob/v0.83.0/internal/providersdkv2/provider.go#L27-L48),
> [v0.84.0 registrations](https://github.com/hashicorp/terraform-provider-hcp/blob/v0.84.0/internal/providersdkv2/provider.go#L27-L48),
> [v0.112.0 registrations](https://github.com/hashicorp/terraform-provider-hcp/blob/v0.112.0/internal/providersdkv2/provider.go#L27-L48).

One quirk that only a real Terraform client catches:
`GetPackerChannelByNameFromList` **lists channels and filters client-side**, so
`ListChannels` is mandatory even though `GetChannel` exists.

A second quirk, verified 2026-07-31: the bucket resource calls
`PackerServiceGetBucket` and `PackerServiceDeleteBucket` **directly** on the
generated client rather than through `internal/clients/packerv2`, so
enumerating that package undercounts the surface. Its destroy path tolerates
exactly one error — an HTTP 404 removes the resource from state; anything else
fails the destroy. (**Probe 2026-07-31:** live `DeleteBucket` on a missing
bucket is literally HTTP 404 with `{"code":5, …}` — the provider's tolerance
and HCP's answer line up, and dufflebag's 404/code-5 matches both.) Its Read also stores `Bucket.ResourceName` in Terraform
state, so the field must carry the spec's documented shape
`packer/project/<project-id>/bucket/<bucket-name>` — an empty value is
perpetual state drift, not a hard failure.
([internal/provider/packer/resources/bucket/resource_packer_bucket.go](https://github.com/hashicorp/terraform-provider-hcp/blob/main/internal/provider/packer/resources/bucket/resource_packer_bucket.go))

---

## Pinned client stack

Per ADR-0010, `hcp-sdk-go` is the
instrument that proves the served contract is correct. Its authority rests entirely on
being **the same artifact Packer links against**, so it must be pinned — and not
alone.

`go-openapi/strfmt` determines how `strfmt.DateTime` serialises, which is
wire-visible. Pin the whole triple.

> **Probe 2026-07-31:** live HCP itself emits *variable* fractional-second
> precision — milliseconds (`.442Z`), microseconds (`.575261Z`) and full
> nanoseconds (`.443428576Z`) appear in the same response document, apparently
> reflecting however each timestamp was stored. Every pinned client stack
> accepts all of them, so the precision dufflebag's strfmt happens to emit is inside
> the envelope real clients already tolerate; the pin still matters for what
> *dufflebag parses*, not for an exact emitted form.

| Packer | `hcp-sdk-go` | `go-openapi/runtime` | `go-openapi/strfmt` |
|---|---|---|---|
| **v1.16.0** (current pin) | **v0.174.0** | **v0.32.3** | **v0.26.3** |
| v1.15.4 | v0.172.0 | v0.28.0 | v0.23.0 |
| v1.14.3 | v0.136.0 | v0.26.2 | v0.21.10 |

Read from each release's `go.mod`. Note the spread: three Packer releases, three
different client stacks.

**Policy**

- **Supported Packer versions begin at v1.15.4.** Deprecated data sources that
  use the 2021-04-30 API are excluded at every supported Packer version.
- **Contract tests** pin the triple to the newest supported Packer — currently
  v1.16.0.
- **Behavioural e2e** *should* run a matrix of real Packer binaries — at
  minimum v1.16.0 and **v1.15.4**, because v1.15.4 is what `hashicorp/tap`
  installs, so it is what a user gets by default, and it links a materially
  older stack. CI runs that behavioural e2e as a matrix over v1.16.0 and
  v1.15.4, with each leg asserting the version it actually drove. Local runs
  choose the binary with `PACKER_E2E_PACKER` and pin the assertion with
  `PACKER_E2E_EXPECT_VERSION`.
- Bumping the pin is a deliberate, reviewable event tied to declaring support for
  a new Packer release. **Exclude these three from automated dependency
  updates**: an auto-bump would not break the contract test, it would silently
  change what the test proves, which is worse.

## Clean-room position

The sources consulted, what was not consulted, and the working rules are
recorded once, in [CONTRIBUTING.md](https://github.com/benemon/dufflebag/blob/main/CONTRIBUTING.md). The rule this
document exists to satisfy: never paste source from `packer/internal/hcp` —
state the contract it implies and cite the file, which is what every section
above does.

## Appendix: live-service probe captures

The numbered captures (`probe NN`) cited throughout were taken against the
live service on 2026-07-31 and 2026-08-01 with a disposable service principal
on an otherwise empty registry, and are retained by the maintainers outside
this repository.
