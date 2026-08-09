#!/bin/sh
# Mints a throwaway CA and a server certificate for the packer gate.
#
# The gate needs real TLS because hcp-sdk-go refuses any auth URL that is not
# https, with no override. Off the lab that used to mean the gate could not run
# at all; here the CA is created in the run, trusted by Packer through
# SSL_CERT_FILE, and thrown away with the work directory. Nothing is shared
# between runs, so there is no secret for a fork's pull request to steal.
#
# The hostname defaults to localhost because it already resolves to loopback
# everywhere, which is the whole of the gate's DNS requirement — no hosts file,
# no sudo, no lab record.
set -eu

dir=${1:?usage: mint-tls.sh <directory> [hostname]}
host=${2:-localhost}

umask 077
mkdir -p "$dir"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
	-subj '/CN=dufflebag test CA' \
	-addext 'basicConstraints=critical,CA:TRUE' \
	-addext 'keyUsage=critical,keyCertSign,cRLSign' \
	-keyout "$dir/ca.key" -out "$dir/ca.pem" 2>/dev/null

openssl req -newkey rsa:2048 -nodes -subj "/CN=$host" \
	-keyout "$dir/tls.key" -out "$dir/tls.csr" 2>/dev/null

# Loopback carries an IP SAN as well as the name: the harness reaches the server
# by hostname, but a client resolving it to 127.0.0.1 and connecting by address
# still has to match.
cat > "$dir/leaf.ext" <<EXT
subjectAltName=DNS:$host,IP:127.0.0.1
extendedKeyUsage=serverAuth
basicConstraints=critical,CA:FALSE
EXT

openssl x509 -req -in "$dir/tls.csr" -days 2 \
	-CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial \
	-extfile "$dir/leaf.ext" -out "$dir/tls.crt" 2>/dev/null

rm -f "$dir/tls.csr" "$dir/leaf.ext"
chmod 600 "$dir/tls.key" "$dir/ca.key"

printf 'minted %s TLS for %s (CA %s)\n' "$dir/tls.crt" "$host" "$dir/ca.pem"
