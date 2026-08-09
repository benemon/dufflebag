# The image wraps the one binary that serves everything (compat planes, /init,
# console over go:embed) — see duf-lza. The web bundle is built here rather
# than taken from the checked-in web/dist so the image never depends on a
# contributor having run make build-ui.

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run typecheck && npm run build

FROM golang:1.26-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /dufflebag ./cmd/dufflebag

# RHEL UBI micro runtime. It ships no CA trust store, so the bundle comes from
# ubi-minimal at the path Go's crypto/x509 reads on RHEL — object-storage TLS
# fails without it. Non-root numeric UID; migrations are embedded in the
# binary, so the same image serves as the init container via the migrate
# subcommand.
FROM registry.access.redhat.com/ubi9/ubi-minimal AS trust

FROM registry.access.redhat.com/ubi9/ubi-micro
COPY --from=trust /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/pki/tls/certs/ca-bundle.crt
COPY --from=build /dufflebag /dufflebag
USER 1001
EXPOSE 8080
ENTRYPOINT ["/dufflebag"]
