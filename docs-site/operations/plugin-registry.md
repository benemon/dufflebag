# Plugin registry

`packer init` normally downloads plugins from GitHub or releases.hashicorp.com.
A plugin registry serves them from dufflebag instead, so builds resolve plugins
that an operator has chosen and that stay available when the internet is not.

Each organization has its own registry. It is off until a maintainer enables it,
and Packer cannot reach it until a maintainer exposes it.

![dufflebag Plugins screen listing the organization's mirrored plugins](/screenshots/plugin-registry.png)

## Requirements

- **Packer 1.16.1 or later.** Earlier Packer releases install plugins only from
  GitHub and refuse a dufflebag source address before sending any request:

  ```
  Invalid github.com URI "dufflebag.example.com/plugins/acme/amazon": a Github-compatible source must be in the github.com/<namespace>/<name> format.
  ```

- **dufflebag reachable on port 443.** A plugin source address cannot carry a
  port, so Packer always connects to port 443 of the host it names. The template
  stanza dufflebag shows names the host without a port.

Source addresses, version constraints and the install sequence are Packer's:
see [Installing plugins](https://developer.hashicorp.com/packer/docs/plugins/install).
- **Object storage.** Plugin files are stored in the deployment's object store.
  See [object storage](../components/object-storage.md).

## Roles

| Action | Role at the organization |
|---|---|
| View the registry and its plugins | `reader` |
| Upload, import, sync, revoke, restore or remove plugin versions | `publisher` |
| Enable, expose, unexpose or disable the registry; set default platforms | `maintainer` |

Packer itself presents no credential. Exposure is what grants it access.

## Enable and expose the registry

1. In the console, open **Plugins** and choose **Enable the registry**.
2. Upload at least one plugin version (below).
3. Open **Registry settings** and choose **Expose**. Every published plugin
   version becomes readable, without credentials, by anything that can reach
   dufflebag.

**Registry settings** also holds the organization's default platforms and the
two other lifecycle controls. **Unexpose** removes that access. A `packer init` in progress gets 404 on its
next request. **Disable registry** deletes every plugin's records, then removes
their files; a file that cannot be removed is logged and is never served. It is
refused while the registry is exposed, so unexpose first.

## Import from releases.hashicorp.com

HashiCorp's own Packer plugins are imported rather than uploaded.

Prerequisites: the `publisher` role, and outbound HTTPS from dufflebag to
`api.releases.hashicorp.com` and `releases.hashicorp.com`.

1. Open **Plugins** and choose **Browse HashiCorp**. The list shows each plugin
   HashiCorp publishes, how many of its versions you mirror, and any plugin
   whose name is held by another source.
2. Choose a plugin. Its releases list the version, release date, lifecycle
   state, platform count, a changelog link, and which versions you already
   hold. Prereleases are hidden unless you show them, and the platform
   filter narrows the list to releases that publish one platform. Tick the
   versions to import.
3. Under **Platforms to import**, tick architectures by operating system. A
   plugin you already mirror preselects the architectures it has; a first
   import preselects the organization's default platforms, which a
   maintainer sets in **Registry settings**. An architecture no chosen
   version publishes cannot be ticked.
4. Choose **Import N versions**. The import runs in the background as one
   job, and its page shows the outcome of each version, with any platform
   that failed.

Before storing a version, dufflebag verifies its SHA256SUMS against
[HashiCorp's release-signing key](https://www.hashicorp.com/.well-known/pgp-key.txt)
(fingerprint `C874 011F 0AB4 0511 0D02 1055 3436 5D94 72D7 468F`). A version
whose signature does not verify is not imported. A version you already mirror
is reported as already mirrored and left unchanged. Serving plugins to Packer
never needs outbound access.

## Import from a GitHub release

Community plugins published as GitHub releases are imported from their link.

Prerequisites: the `publisher` role; outbound HTTPS from dufflebag to
`api.github.com` and `github.com`; a public release that includes a SHA256SUMS
asset.

1. Open **Plugins** and choose **Import from GitHub**.
2. Paste a release link, `https://github.com/<owner>/packer-plugin-<name>/releases/tag/<tag>`
   or `.../releases/latest`, and choose **Resolve**. dufflebag shows what it
   inferred: the repository, the plugin name, the version and tag, the
   release date, the checksum file, and whether the name is new, already
   mirrored, or held by another source. A `latest` link is resolved to its
   tag now, and the import uses that tag even if a newer release appears.
   A release without a SHA256SUMS asset cannot be imported.
3. Tick platforms; a first import preselects the organization's defaults
   and a mirrored plugin the architectures it has. Choose **Import**. The
   import's page shows the outcome.

GitHub allows 60 unauthenticated requests an hour from one address. When
they are used up, the page says when the limit resets; mirrored plugins
are unaffected.

A release without a SHA256SUMS asset cannot be verified or served. GitHub
releases carry no key dufflebag can check, so a signature asset is kept as
published but not verified. Resolving and importing call the GitHub API
without credentials, under
[GitHub's unauthenticated rate limit](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api);
when that is spent, the refusal names the time it resets. Downloads of release
files do not count against it.

## Upload a plugin version

A version is uploaded as its release files:

- the version's SHA256SUMS file, required;
- the plugin zips, one per platform;
- the detached signature (`..._SHA256SUMS.sig`), if the release has one;
- the manifest (`..._manifest.json`), if the SHA256SUMS file lists one.

Releases from releases.hashicorp.com list a manifest;
[goreleaser](https://goreleaser.com/customization/checksum/) releases on GitHub
do not.

In the console, open **Plugins**, choose **Upload plugin files**, and drop or
select the files for one version or several. The console groups them into one
upload per plugin version and, before anything is sent, shows each version as
new or already mirrored and each file as new, a new architecture, or already
mirrored; a version with no SHA256SUMS file or no zip, and any file that is
not a release file, is listed under **Not sent**. A name held by another
source refuses the whole upload. Each version is then uploaded separately and
shows its own result, so one refusal does not stop the others. The API takes
one version's files as one multipart request:

```shell
curl -X PUT "https://dufflebag.example.com/api/v1/organizations/$ORG_ID/plugin-registry/plugins/git/versions/0.6.3" \
  -H "Authorization: Bearer $TOKEN" \
  -F sha256sums=@packer-plugin-git_v0.6.3_SHA256SUMS \
  -F zips=@packer-plugin-git_v0.6.3_x5.0_linux_amd64.zip
```

dufflebag verifies the whole file set before storing anything. Every uploaded
zip must be listed in the SHA256SUMS file with a matching digest, and its name
must be one Packer accepts. A refusal names the file that failed.

Versions are immutable: uploading a version that already exists is refused.
A plugin name belongs to one source per organization.

Uploads are limited by `DFBG_PLUGIN_MAX_UPLOAD_BYTES` (512 MiB by default). An
upload may take up to 30 minutes. A reverse proxy in front of dufflebag applies
its own limits: an OpenShift route times out after 30 seconds unless the
`haproxy.router.openshift.io/timeout` annotation raises it.

## Change a plugin's versions and platforms

A publisher changes what a plugin serves from its page:

1. Choose **Edit versions**.
2. Untick a version to revoke it, or tick a revoked version to restore it.
   Packer gets 404 for each file of a revoked version, and the files are
   kept. A template pinned to that version fails until it is restored.
3. For an imported plugin, tick an unmirrored platform (○) on a served
   version to add it. A plugin from releases.hashicorp.com also lists its
   newer releases; tick one to mirror it with the platforms the plugin
   already has.
4. Check the pending changes under the grid; **Undo** drops one. Choose
   **Sync N changes**. The changes run in order as one job, and its page
   shows each change's outcome. **Discard** leaves edit mode without
   syncing.

![The plugin detail grid in edit mode with one pending change](/screenshots/plugin-detail-edit.png)

![A sync job page showing each change and its outcome](/screenshots/plugin-import-job.png)

A job's page names who started it and from where, its place in the queue
while it waits, and each version or change with its outcome; a platform
that failed or was already mirrored is listed under its version with the
reason. A failed job offers **Retry job**; a partially successful one offers
**Retry failed changes**, which queues a new job for the failed part only.

A platform added to a mirrored version is admitted only if the SHA256SUMS
stored when the version was first mirrored lists it with the same digest. A
release changed upstream since then cannot add to it. Mirrored platforms
cannot be removed: revoke the version, or remove it and import it again
without that platform.

**Remove version**, in a version's row menu, deletes its records, then
removes its files; a file
that cannot be removed is logged and is never served. It cannot be undone.
Removing a plugin's last version removes the plugin, and its name can then
be used by another source. A revoked version still holds the name.

The API equivalents are `POST .../plugins/{name}/sync` with a list of
changes, `POST .../versions/{version}/revoke`,
`POST .../versions/{version}/restore` and `DELETE .../versions/{version}`.

## Check for updates

A plugin imported from releases.hashicorp.com or GitHub can check its source
for newer releases. Uploaded plugins have no source to check.

1. Open the plugin and turn on **Check daily for a newer stable version**
   in its Update check card, which also shows when the last check ran and
   its last error.
2. dufflebag asks the source for its newest stable release within a minute,
   then once every `DFBG_PLUGIN_UPDATE_INTERVAL` (24 hours by default).
   Prereleases are ignored.
3. When that release is newer than every version the plugin holds, revoked
   ones included, the catalogue marks the plugin **Update available**.

A check only looks. Nothing is imported until you sync. When a check first
sees a newer release, the organization's own
[webhooks](../administration/webhooks.md#organization-events) receive one
`plugin.update_available` event. To bring plugins up to date, tick them in
the catalogue (the **Update available** filter shows only those) and choose
**Sync selected**. The confirmation lists each plugin's move and any of its
platforms the new release does not publish, then **Start N jobs** queues one
import job per plugin for the newest release, with the platforms it already
has, so one plugin's failure does not hold back the rest. A GitHub plugin is
imported from the release tag the check saw.

A failed check is shown on the plugin's page with its reason and is retried
at the next interval. It raises no alert, so a deployment without internet
access simply never sees an update. GitHub checks share the 60 calls an hour
that imports use: dufflebag makes at most one every two minutes, and none for
an hour after GitHub refuses one.

## Platforms

The SHA256SUMS file may list platforms whose zips you did not upload. dufflebag
serves only the zips it holds. The plugin's page in the console shows, for each
version, which platforms are held (●), listed but not held (○), or not
published (–).

On a platform with no zip, Packer moves to the next older version that the
template's [version constraint](https://developer.hashicorp.com/packer/docs/plugins/install)
allows and that has the platform. An exact
constraint such as `version = "0.6.3"` allows no other version, so the
install fails on that platform. A range such as `version = ">= 0.6.0"` can
install an older version there.

## Use a plugin in a template

The upload result and the plugin's page show the template stanza, pinned to the
newest available version:

```hcl
packer {
  required_plugins {
    git = {
      source  = "dufflebag.example.com/plugins/acme/git"
      version = "0.6.3"
    }
  }
}
```

The source is the dufflebag host, `plugins`, the organization name and the
plugin name. Run `packer init` as usual.

## Audit

Every read Packer makes is recorded in the audit log, with route
`root.plugins`, operation `plugin.read` and the requested file as the target.
When audit is enabled and a record cannot be written, the read is refused, so
an audit outage stops `packer init`. See [audit](../administration/audit.md).

## Where to go next

- [Object storage](../components/object-storage.md)
- [Audit](../administration/audit.md)
- [Installation settings](../quick-start/installation.md)
