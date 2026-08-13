# Platform API

The platform API manages dufflebag tenancy, service principals and operational
configuration. Its OpenAPI document is the source for endpoint, request and
response details; see the [generated API reference](/platform-api.html).

## Authentication

Exchange a service principal's client ID and secret at `/oauth2/token` using
the OAuth 2.0 client-credentials grant. Send the returned access token on
authenticated requests as a bearer token.

## Tenancy

An organisation contains projects. A principal can be scoped to the platform,
an organisation, or one project. Organisation-scoped principals can act across
that organisation's projects; project-scoped principals can act only in their
project.

## Roles

Roles are ordered tiers within a principal's scope:

- `reader` provides read access.
- `builder` is the lowest tier with write access.
- `publisher` is the publishing tier.
- `maintainer` provides administrative access within the scope.
- `root` provides platform administration.

A caller can grant or modify only roles at or below its own tier. The
[generated API reference](/platform-api.html) records the required role for
each operation.
