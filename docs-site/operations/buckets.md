# Buckets

A bucket is the unit of registry organisation: Packer publishes into one, and
its versions, channels and builds hang off it. This page covers the Buckets
list screen and the single-bucket drill-down.

## The Buckets screen

The **Buckets** screen lists the project's registry.

![Dufflebag Buckets screen showing the project registry](/screenshots/buckets.png)

Builders can pin buckets and unpin them from the pinned card. Publishers can
delete a bucket.

::: warning
When Bag Drop currently mirrors a bucket, the console warns that deleting the
bucket also deletes it from the destination. See
[Bag Drop](../administration/bag-drop.md).
:::

## A bucket

A bucket opens onto its versions and channels. The versions table shows
completion and revocation state.

![Dufflebag bucket screen showing bucket details, ancestry, versions, and channels](/screenshots/bucket-facets.png)

The versions table is covered in [Versions](./versions.md); the channels tab
in [Channels](./channels.md).

Buckets are created by a `packer build` (see
[Build an image with Packer](../quick-start/build-with-packer.md)) or by the
`hcp_packer_bucket` Terraform resource — the console deliberately offers no
bucket creation, matching HCP's own console.
