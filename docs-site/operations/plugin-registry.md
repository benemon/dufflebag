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
| View the registry, its plugins, its jobs and its checks | `reader` |
| Upload, import, sync, revoke, restore or remove plugin versions; clear jobs; turn checks on or off | `publisher` |
| Enable, expose, unexpose or disable the registry; set default platforms; schedule pruning | `maintainer` |

Packer itself presents no credential. Exposure is what grants it access.

## Enable and expose the registry

1. In the console, open **Plugins** and choose **Enable the registry**.
2. Upload at least one plugin version (below).
3. Open **Plugin registry** under **Administration** and choose **Expose**. Every published plugin
   version becomes readable, without credentials, by anything that can reach
   dufflebag.

Once exposed, the **Plugin registry** screen shows the address Packer resolves the
organization's plugins from, `https://<host>/plugins/<organization>`, with a
`required_plugins` stanza to copy; each plugin's own page shows the stanza
pinned to its newest available version.

![The Plugin registry administration screen: lifecycle, default platforms and the prune schedule](/screenshots/plugin-registry-settings.png)

**Plugin registry** (under **Administration**) also holds the organization's default platforms, ticked
from the platforms HashiCorp builds Packer plugins for (grouped by operating system), and the
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
   whose name is held by another source or reserved by a queued import.
2. Choose a plugin. Its releases list the version, release date, lifecycle
   state, platform count, a changelog link, and which versions you already
   hold. Prereleases are hidden unless you show them, and the platform
   filter narrows the list to releases that publish one platform. Tick the
   versions to import.
3. Under **Platforms to import**, tick architectures by operating system. A
   plugin you already mirror preselects the architectures it has; a first
   import preselects the organization's default platforms, which a
   maintainer sets under **Administration**, **Plugin registry**. An architecture no chosen
   version publishes cannot be ticked.
4. Choose **Import N versions**. The import runs in the background as one
   job, and its page shows the outcome of each version, with any platform
   that failed. An import identical to one already queued or running is
   refused on the spot, naming that job.

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
   mirrored, held by another source, or reserved by a queued import. A `latest` link is resolved to its
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

Uploads are limited by `DFBG_PLUGIN_MAX_UPLOAD_BYTES` (4 GiB by default); a
plugin bundle import shares the limit. The body is spooled to a file under
the server's temporary directory while it is checked, so the host or pod
needs that much scratch space. An upload may take up to 30 minutes. A reverse proxy in front of dufflebag applies
its own limits: an OpenShift route times out after 30 seconds unless the
`haproxy.router.openshift.io/timeout` annotation raises it.

## Change a plugin's versions and platforms

A publisher changes what a plugin serves from its page. The Versions
card lists the mirrored versions, each with a coverage strip (one mark
per platform: mirrored, revoked, to be added, upstream, not published)
and a count; expand a row to see its platforms by operating system.

1. **Revoke** a version from its row, or **Restore** a revoked one. Each
   asks for confirmation and applies at once. Packer gets 404 for each
   file of a revoked version, and the files are kept. A template pinned
   to that version fails until it is restored.
2. For an imported plugin, expand a served version and tick an upstream
   platform to add it. A plugin from releases.hashicorp.com also names
   its newer releases above the list: **Add x.y.z with N platforms**
   mirrors the newest with the platforms the plugin already has, and
   **All** shows every upstream version, each with **Add with N
   platforms** or its own platforms to tick.
3. Each addition lands in the pending tray under the list; close a chip to
   drop it. Choose **Sync N changes**. The additions run in order as one
   job, and its page shows each one's outcome.

![The Revoke confirmation over the plugin detail versions list](/screenshots/plugin-detail-revoke.png)

![An import job page showing the imported version and its platforms](/screenshots/plugin-import-job.png)

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

## Jobs

Every import and sync runs as a job: queued, then run by the server one at
a time in the order queued, whether or not anyone is watching. The **Jobs**
facet of the Plugins screen lists the organization's jobs newest first,
with each one's state, what it does, who queued it and from where, and the
first error of a failed one. Filter by state or plugin; a failed or partly
failed job offers **Retry** to publishers. The facet's badge counts the
jobs queued or running.

![The Jobs facet listing the organization's import and sync jobs](/screenshots/plugin-jobs.png)

Publishers can **clear** jobs: tick them and choose **Clear N jobs**. A
queued job is taken out of the queue and never runs; a finished job's
record is deleted and the versions it mirrored are unchanged. A running job
cannot be cleared, and a queued one that starts running before the clear
lands keeps running. A cleared job's page says so. Each clear is audited.

A plugin with a queued or running job shows it on its catalogue row in
place of the update pill, and its page carries a notice with the job's
progress that turns to the outcome when it finishes. A first import whose
plugin has no version yet still has a row, marked **None yet**. Queuing an
import identical to one already queued or running is refused: the import
screen names that job and offers to open it. A first import also reserves
its name, so the same plugin cannot be queued from another source until
the job finishes.

## Prune jobs

Finished job records can be cleared on a schedule. It is off by default;
jobs are kept until a publisher clears them. A maintainer turns it on
under **Administration › Plugin registry › Prune jobs**, choosing an age
limit (1 to 365 days, counted from when each job finished) and a schedule
as a five-field cron expression read in UTC (`0 3 * * *`, every day at
03:00, by default); the card reads the expression back in plain English
as you type. It previews what a run would clear now,
shows the next and last runs and the last error, and offers **Prune
now**. Queued and running jobs are never pruned. A run happens once per
organization, whatever the number of replicas, and writes one audit
event naming how many jobs it cleared. A pruned job's page says it was
cleared.

![The Prune jobs card: the switch, the age limit, the schedule read back in English, the preview and the last run](/screenshots/plugin-registry-prune.png)

## Check for updates

A plugin imported from releases.hashicorp.com or GitHub can check its source
for newer releases. Uploaded plugins have no source to check.

1. Open the plugin and turn on **Check daily for a newer stable version**
   in its Update check card, which also shows when the last check ran and
   its last error; or flip the plugin's switch in the **Checks** facet.
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

The **Checks** facet of the Plugins screen lists every imported plugin
with its check switch, when it was last checked, the result (up to date, a
newer version, or the failure's reason) and when the next check is due;
its tabs filter to the plugins with a newer version, the failed checks and
the plugins with checks off. Uploaded plugins are counted but not listed:
an upload has no upstream.

![The Checks facet listing each imported plugin's last check and next due time](/screenshots/plugin-checks.png)

A failed check is shown on the plugin's page with its reason and is retried
at the next interval. It raises no alert, so a deployment without internet
access simply never sees an update. GitHub checks share the 60 calls an hour
that imports use: dufflebag makes at most one every two minutes, and none for
an hour after GitHub refuses one.

## Downloads

One download is one plugin zip served completely to `packer init`. Version
indexes, SHA256SUMS files, signatures, manifests, interrupted transfers and
missing files do not count. Counts are kept per plugin version and platform.

The Downloads card on the plugin page shows totals for all versions or one
version. Its rolling windows cover the last 7, 30 and 365 UTC calendar days,
ending today, plus all time. Removed and revoked versions keep their counts and
remain available in the version selector with their state.

![A plugin detail page showing the Downloads card and rolling windows](/screenshots/plugin-detail.png)

## Plugin bundle

A plugin bundle carries the whole registry across an airgap. It is a
`tar.gz` of every plugin's release files, whatever their source, in the
layout the upload API accepts, so a disconnected dufflebag needs nothing it
does not already have to take it in. Revoked versions are left out, and a
plugin whose versions are all revoked is left out entirely. The airgapped
registry keeps its own view of what is revoked: a revocation on the
connected side after a bundle is imported does not propagate.

A maintainer exports under **Administration › Plugin registry › Plugin
bundle**. The export runs in the background and the card shows the current
bundle: its filename, when it was exported, its size, how many plugins and
versions it holds, and its SHA-256, copyable. One export runs at a time, and
a new export replaces the previous bundle, so an organization holds one
bundle. A failed export keeps the previous bundle and says why it failed.

**Download** saves the bundle through the browser. For a large bundle, the
card shows the curl form:

```shell
curl -H "Authorization: Bearer $TOKEN" -o dufflebag-plugins-acme-20261013T090040Z-0123456789ab.tar.gz \
  https://dufflebag.example.com/api/v1/organizations/$ORG_ID/plugin-registry/bundle/download
```

The filename ends in the first twelve characters of the archive's SHA-256,
and the card shows the full digest, so a copied bundle can be matched to the
export it came from without a second file. Inside, `manifest.json` comes
first and names the exporting instance, the export time, and every plugin
with its origin, versions, platforms and file digests; the release files
follow under `plugins/<name>/<version>/`.

![The Plugin bundle card showing an exported registry with its digest and Download](/screenshots/plugin-registry-bundle.png)

Importing a bundle on a disconnected dufflebag is the subject of a later
release.

## Platforms

The SHA256SUMS file may list platforms whose zips you did not upload. dufflebag
serves only the zips it holds. The plugin's page shows each version's coverage
as a strip of one mark per platform, in the legend's words: mirrored, revoked,
will be added, upstream (published but not held) or not published; expand the
row to read the same by operating system.

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
an audit outage stops `packer init`. Reading a plugin's download counts is
the platform operation `plugin.downloads.read`, with the plugin as its
target. A bundle export is `plugin_registry.bundle.export`, reading its
state `plugin_registry.bundle.read`, the download
`plugin_registry.bundle.download`, and the build itself is recorded by
`system:plugin-bundler` as `plugin_registry.bundle.build`. See
[audit](../administration/audit.md).

## Where to go next

- [Object storage](../components/object-storage.md)
- [Audit](../administration/audit.md)
- [Installation settings](../quick-start/installation.md)
