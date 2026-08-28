# Object storage

SBOMs live in an S3-compatible object store. Without one configured, the
server starts and serves everything except SBOM upload and download, which
answer 503 - a reasonable way to evaluate dufflebag before committing storage
to it. The bucket must already exist, and the server verifies it is reachable
at startup. Vulnerability scanning stores its transcripts here too, so a
configured scanner without object storage is a startup error, not a degraded
mode.

## Configuration

All five values are optional as a group. If any is set, every value must be
valid and the bucket must already exist. A configured but unavailable store
is a startup error.

| Variable | Description |
|---|---|
| `DFBG_OBJECT_STORAGE_ENDPOINT` | S3-compatible endpoint |
| `DFBG_OBJECT_STORAGE_REGION` | Region supplied to the S3 client |
| `DFBG_OBJECT_STORAGE_BUCKET` | Existing bucket for SBOM payloads |
| `DFBG_OBJECT_STORAGE_ACCESS_KEY` | Access key |
| `DFBG_OBJECT_STORAGE_SECRET_KEY` | Secret key |

The browser smoke test, real-Packer lane and object-store integration tests
use [Ceph RGW's S3-compatible API](https://docs.ceph.com/en/latest/radosgw/s3/).

## Transcript lifecycle

Scan transcripts are written to object storage tagged
`dufflebag-class=transcript`. dufflebag deletes each referenced transcript
after its seven-day window, but two narrow crash windows can leave an object
behind with no database row referencing it - storage waste, not a
correctness or disclosure problem. To collect those strays, configure a
bucket lifecycle rule filtering on that tag with an expiry comfortably past
the seven days (say 14). Referenced transcripts are gone before the rule
fires, and orphans age out on their own. Never apply an expiry to untagged
objects - SBOMs live in the same bucket and live forever.
