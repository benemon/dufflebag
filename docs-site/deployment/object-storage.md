# Object storage

SBOMs live in an S3-compatible object store. Without one configured, the
server starts and serves everything except SBOM upload and download, which
answer 503 — a reasonable way to evaluate dufflebag before committing storage
to it. Configure it with the `DFBG_OBJECT_STORAGE_*` variables in the configuration
reference; the bucket must already exist, and the server verifies it is
reachable at startup. Vulnerability scanning stores its transcripts here too,
so a configured scanner without object storage is a startup error, not a
degraded mode.

