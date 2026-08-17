# Buckets

A bucket is the unit of registry organisation: Packer publishes into one, and
its versions, channels and builds hang off it. This page covers choosing a
bucket and the single-bucket drill-down.

## The Buckets screen

The **Buckets** screen lists the project's registry: every bucket with its
latest version, channels and state, with pinned buckets gathered above the
list. Organisation- and project-scoped sessions land here; a bucket-scoped
principal has exactly one bucket and lands straight in it instead.

![Dufflebag Buckets screen showing the project registry](/screenshots/buckets.png)

Builders can pin buckets and unpin them from the pinned card or the bucket's
own header. Publishers can delete a bucket from either place.

## Choosing a bucket

Bucket selection also lives in the masthead beside the organisation and
project pickers: a searchable drop-down that follows the route, so the bucket
in the address bar is always the bucket on screen. Pinned buckets group first;
typing filters the list. For a bucket-scoped session the picker is the
orientation point — it always names the bucket the session landed in.

![Dufflebag bucket picker open in the masthead, pinned buckets grouped first](/screenshots/bucket-picker.png)

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
a console-created bucket is indistinguishable from a published one. For a
bucket-scoped session the action is disabled with the reason stated: creating
a bucket changes the set of buckets rather than acting inside the one the
session is bound to, and the server refuses it whatever the role.
