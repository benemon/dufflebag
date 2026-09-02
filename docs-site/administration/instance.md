# Instance

The **Instance** screen is available to every reader. It generates the
`HCP_*` client environment exports for the selected tenancy - the block that
points stock Packer and Terraform at the instance - and reports scanner and
build information.

![dufflebag Instance screen showing health and the client environment block](/screenshots/instance.png)

Export the generated block and use it as
[Build an image with Packer](../quick-start/build-with-packer.md) and
[Manage dufflebag with Terraform](../quick-start/manage-with-terraform.md)
describe. The variables themselves are documented in the
[client redirection reference](../quick-start/installation.md#client-redirection).
For a bucket-scoped session the block also exports
`HCP_PACKER_BUCKET_NAME` set to the session's bucket - the fallback bucket
name Packer reads when the template names none, and the only bucket the
credential can publish into. Wider sessions never emit it, even while viewing
a bucket. The credential is not bound to one. If the bucket cannot be
resolved, the console says so above the block rather than emitting an
incomplete one silently.

![dufflebag Instance screen in a bucket-scoped session showing the Bucket navigation entry and HCP_PACKER_BUCKET_NAME](/screenshots/instance-bucket-scoped.png)

::: warning
The console warns if it is not served over HTTPS. Authentication requires an
HTTPS URL.
:::

The screen also surfaces the instance's health, mirroring the unauthenticated
`GET /sys/health` probe described under
[Serving and readiness](../quick-start/installation.md#serving-and-readiness),
including the encryption heartbeat state on encrypted deployments (see
[Encryption - health](../components/encryption.md#encryption-health)).
