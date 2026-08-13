# Platform API

The platform API manages dufflebag tenancy, service principals, and
operational configuration.

::: info
The OpenAPI document defines endpoint, request, and response details. See the
[generated API reference](/platform-api.html).
:::

## Authentication

Prerequisites: A service principal's client ID and secret.

1. Exchange the client ID and secret at `/oauth2/token` with the OAuth 2.0
   client-credentials grant.

2. Send the returned access token as a bearer token on authenticated requests.

## Tenancy

An organisation contains projects. A principal has platform, organisation, or
project scope. An organisation-scoped principal can act across that
organisation's projects. A project-scoped principal can act only in its
project.

## Roles

Roles are ordered tiers within a principal's scope:

- `reader` provides read access.
- `builder` is the lowest tier with write access.
- `publisher` is the publishing tier.
- `maintainer` provides administrative access within the scope.
- `root` provides platform administration.

::: warning
A caller can grant or modify only roles at or below its own tier.
:::

The [generated API reference](/platform-api.html) records the required role for
each operation.
