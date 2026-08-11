#!/usr/bin/env python3
# Overlay: changes Version revoke_at to x-nullable true and x-omitempty false.
# Why: the live wire renders null for never-revoked versions (dossier capture
# A.7 and the S3a live proof), while the published spec omits nullability; the
# dossier outranks the spec for external behaviour. The vendored artifact
# remains pristine and checksummed; this overlay edits only a generate-time copy.

import json
import pathlib
import sys


if len(sys.argv) != 2:
    raise SystemExit("usage: hcp2023-version-revoke-at.py SPEC_COPY")

path = pathlib.Path(sys.argv[1])
document = json.loads(path.read_text(encoding="utf-8"))
revoke_at = document["definitions"]["hashicorp.cloud.packer_20230101.Version"]["properties"]["revoke_at"]
if revoke_at.get("type") != "string" or revoke_at.get("format") != "date-time":
    raise SystemExit("Version revoke_at no longer has the expected date-time shape")
if "x-nullable" in revoke_at or "x-omitempty" in revoke_at:
    raise SystemExit("Version revoke_at overlay is already present in the vendored copy")

revoke_at["x-nullable"] = True
revoke_at["x-omitempty"] = False
path.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
