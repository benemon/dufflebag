# Vendored specification provenance

Per the spec-first rule ([architecture](../../docs/architecture.md)), these specifications are the
source of truth for dufflebag's compatibility plane. They are transcribed, never
edited.

## Provenance rule — public sources only

These files are obtained from the public documentation download at
`https://developer.hashicorp.com/hcp/api-docs/packer`, using the "Download Spec"
control. That is the only permitted source, and the checksums below are how a
reader confirms it.

The clean-room position in [CONTRIBUTING.md](../../CONTRIBUTING.md) rests on
every input being publicly available, so where a specification is published it
is taken from where it is published.

## Overlays

Vendored specification files remain pristine, checksummed copies of their
public downloads. Overlays never modify those files: generation copies a
vendored specification to a temporary path, applies the overlay to that copy,
and generates models from the copy. `make generate-check` invokes the same
`make generate` target, so stale generated output or an overlay failure fails
the check.

Each overlay documents the exact transformation and the external evidence that
requires it. `spec/overlays/hcp2023-version-revoke-at.py` makes the 2023-01-01
Version `revoke_at` property nullable and non-omitted based on dossier capture
A.7 and the S3a live proof.

## Vendored files

### cloud-packer-service 2023-01-01

| | |
|---|---|
| File | `cloud-packer-service/2023-01-01/hcp.swagger.json` |
| Title | HashiCorp Cloud Platform Packer Artifact Registry |
| Format | Swagger 2.0 |
| Retrieved | 2026-07-29, public "Download Spec" control |
| SHA-256 | `fac6fd73a53318c8828776d2f6c5466b6b29160397e696209057683f79f33f6f` |
| Contents | 29 paths, 52 operations, 132 definitions |

Independently corroborates the operation count derived by reading
`hcp-sdk-go`'s generated client (52).

### cloud-packer-service 2021-04-30

| | |
|---|---|
| File | `cloud-packer-service/2021-04-30/hcp.swagger.json` |
| Title | HashiCorp Cloud Platform Packer Artifact Registry |
| Format | Swagger 2.0 |
| Retrieved | 2026-07-29, public "Download Spec" control |
| SHA-256 | `fa0ed9ec2adfb3b8c323cbfb7243bc475e391317cc8f6cc0eefefabd6e9fb090` |
| Contents | 15 paths, 28 operations, 73 definitions |

Independently corroborates the 28 operations counted from the generated client.
At the 2026-07-29 enumeration, two were reachable from the deprecated Packer
data sources:

```
GET .../images/{bucket_slug}/iteration          ?iteration_id= | ?fingerprint= | ?incremental_version=
GET .../images/{bucket_slug}/channels/{slug}
```

`GetIteration` is a three-way selector. Packer exposes option constructors for
only two of them — `GetIteration_byID` and `GetIteration_byFingerprint` — so
`incremental_version` is never set by the CLI, though the spec defines it.

**Enumeration amendment, 2026-08-01:** the specification remains vendored,
checksummed, and monitored as reference evidence, but zero of its operations
are served. ADR-0013 now bounds coverage by supported client versions: the
deprecated Packer data sources are unsupported and terraform-provider-hcp
v0.84.0 or newer no longer registers their equivalents.

### cloud-resource-manager 2019-12-10

| | |
|---|---|
| File | `cloud-resource-manager/2019-12-10/hcp.swagger.json` |
| Title | HashiCorp Resource Manager Service |
| Format | Swagger 2.0 |
| Retrieved | 2026-07-29, public "Download Spec" control |
| SHA-256 | `5d5df07c13e4d1c214a56202ecd0cccf98260ec6f46e08bf4a4c514a17523236` |
| Contents | 27 paths, 35 operations, 89 definitions |

This is **pure HCP platform infrastructure, not Packer** — but the Packer CLI is
a consumer of it. `loadOrganizationID()` calls `OrganizationService_List` when
`HCP_ORGANIZATION_ID` is unset, and `loadProjectID()` calls `ProjectService_List`
when `HCP_PROJECT_ID` is unset.

**We implement 3 of its 35 operations.** We are not adopting the Resource Manager
service; we are serving three GETs that happen to live in its URL space. The
coverage rule in [architecture](../../docs/architecture.md) works in both
directions here — it puts these three in scope, and puts the other 32
definitively out of it. The third was added after the provider-vs-Packer
correction recorded in the dossier section 2: provider v0.112.0 fetches a
pinned project even though Packer skips discovery when both ids are pinned.

```
GET /resource-manager/2019-12-10/organizations   ?pagination.page_size= &pagination.next_page_token=
GET /resource-manager/2019-12-10/projects        ?scope.type= &scope.id= &pagination.* &sorting.order_by= &query=
GET /resource-manager/2019-12-10/projects/{id}
```

The List operations are reachable in the *unpinned* case; ProjectService_Get is
reachable from the provider in the pinned-project case. All are stateless reads over data
[architecture](../../docs/architecture.md) already requires us to model as
first-class, so marginal cost is near zero. Pagination is real and the response
envelopes carry it.

## All required specs are vendored

## What the spec does not tell you

Verified against this file on 2026-07-29:

| Spec says | Actual behaviour |
|---|---|
| `Version.name` — *"Human-readable name of the version."* | Load-bearing. `name != "v0"` means **complete**, and Packer aborts |
| `default` response → `google.rpc.Status` | Correct, but the *code values* matter: `5` for a missing bucket, `10` for a missing version |
| — | Zero occurrences of `v0`, `Aborted`, or `incomplete` anywhere in the document |

A spec-only implementation would abort rather than create versions, and fail on
every subsequent build against a given fingerprint. The spec defines structure;
[compatibility.md](../../docs/compatibility.md) defines
semantics. Both are required, and where they disagree, **observed behaviour
wins**.

## Archaeology: the 2021-04-30 spec explains the `v0` sentinel

The legacy API modelled version completion with **two** well-designed fields:

| Field | Type | Documented as |
|---|---|---|
| `complete` | boolean | *"If true, all builds associated with this iteration have successfully completed and uploaded metadata to the registry. When complete is true, this iteration is considered ready to use, and can have channels assigned to it."* |
| `incremental_version` | integer | *"The human-readable version number assigned to this iteration. This field will only be set if the iteration is complete."* |

2023-01-01 collapsed both into the single `name` string: `"v0"` when incomplete,
`"v<N>"` when complete. The sentinel is not an accident of implementation — it is
a lossy migration of two fields into one.

Three consequences:

1. **The domain model is settled, not inferred.** `Version{Complete bool,
   Sequence int}` is what the old API already said. Per
   the wire-model rule ([architecture](../../docs/architecture.md)), adapters project it:
   the 2021-04-30 adapter emits both fields directly; the 2023-01-01 adapter
   renders `name`.
2. **A domain invariant we did not previously know:** channels may only be
   assigned to *complete* versions.
3. **Completion has a definition:** all builds succeeded *and* uploaded metadata
   — not merely "the run finished".
