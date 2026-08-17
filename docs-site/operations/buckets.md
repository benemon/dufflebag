# Buckets

A bucket is the unit of registry organisation: Packer publishes into one, and
its versions, channels and builds hang off it. This page covers choosing a
bucket and the single-bucket drill-down.

## Choosing a bucket

Bucket selection lives in the masthead beside the organisation and project
pickers: a searchable drop-down that follows the route, so the bucket in the
address bar is always the bucket on screen. Pinned buckets group first; typing
filters the list.

![Dufflebag bucket picker open in the masthead, pinned buckets grouped first](/screenshots/bucket-picker.png)

With no bucket chosen, the registry landing says so and points at the picker.

![Dufflebag registry landing prompting bucket choice](/screenshots/buckets.png)

Builders can pin a bucket and unpin it from the bucket's own header — pinned
buckets surface first in the picker. Publishers can delete a bucket from the
same header.

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
[Build an image with Packer](../quick-start/build-with-packer.md)), by the
`hcp_packer_bucket` Terraform resource, or from the picker's **Create bucket**
action — which issues the same compatibility-plane request a client would, so
a console-created bucket is indistinguishable from a published one.
