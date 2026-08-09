# Creates the bucket a test deployment expects to find, from inside the Ceph
# container. Dufflebag never creates buckets — a deployment provisions them —
# so this belongs to the harness rather than to the code under test.
#
# It signs the request by hand because the image carries no S3 client: no aws,
# no s3cmd, no mc, and no boto3. Only python3 and curl, and SigV4 is beyond
# curl without a signature to hand it.
import datetime, hashlib, hmac, sys, urllib.request

access, secret, bucket = sys.argv[1], sys.argv[2], sys.argv[3]
host, region, service = "127.0.0.1:8000", "us-east-1", "s3"
now = datetime.datetime.now(datetime.timezone.utc)
stamp, date = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
payload = hashlib.sha256(b"").hexdigest()

canonical = "\n".join([
    "PUT", "/" + bucket, "",
    "host:" + host, "x-amz-content-sha256:" + payload, "x-amz-date:" + stamp, "",
    "host;x-amz-content-sha256;x-amz-date", payload,
])
scope = "/".join([date, region, service, "aws4_request"])
to_sign = "\n".join([
    "AWS4-HMAC-SHA256", stamp, scope,
    hashlib.sha256(canonical.encode()).hexdigest(),
])

key = ("AWS4" + secret).encode()
for part in (date, region, service, "aws4_request"):
    key = hmac.new(key, part.encode(), hashlib.sha256).digest()
signature = hmac.new(key, to_sign.encode(), hashlib.sha256).hexdigest()

request = urllib.request.Request("http://" + host + "/" + bucket, method="PUT")
request.add_header("Host", host)
request.add_header("x-amz-content-sha256", payload)
request.add_header("x-amz-date", stamp)
request.add_header("Authorization",
    "AWS4-HMAC-SHA256 Credential=" + access + "/" + scope +
    ", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=" + signature)
try:
    with urllib.request.urlopen(request) as response:
        print("created", response.status)
except urllib.error.HTTPError as err:
    body = err.read().decode()
    if err.code == 409 or "BucketAlreadyOwnedByYou" in body:
        print("exists")
    else:
        print("failed", err.code, body); sys.exit(1)
