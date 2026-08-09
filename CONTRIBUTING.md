# Contributing to dufflebag

## Before designing anything

Ask *"what is the simplest machinery that enforces this contract?"* before
*"how do we design X?"* — the frame set first determines the whole
conversation. At every decision, name the delete option: what does this look
like if we drop the component entirely? If the answer costs a rare feature
that carries its own lifecycle, deletion is usually right. The target is few
moving parts, well tested — not "production-grade", which biases toward
machinery.

The boundaries a change must respect are in
[docs/architecture.md](docs/architecture.md). For anything the stock Packer
client or `terraform-provider-hcp` touches, the
[compatibility reference](docs/compatibility.md) is authoritative, and
observed client behaviour is authoritative over published documentation.

## Clean room

dufflebag reimplements the server side of an API that existing clients
already speak. The position, and the rules that keep it sound:

- The HCP Packer **server** source is not published, and the implementers did
  not access or consult non-public server source. Everything consulted is
  client-side or public documentation:

  | Source | Nature | Licence |
  |---|---|---|
  | HCP Packer public API documentation | Published interface spec | Public docs |
  | HCP Packer user documentation | Workflows, features, UX | Public docs |
  | `hashicorp/hcp-sdk-go` | Generated API client + models + config | MPL-2.0 |
  | `hashicorp/packer` client integration | Observable client behaviour | BUSL-1.1 |
  | Wire traffic from a real `packer` run | Observed protocol | n/a |

- Reading a client to determine what a server must do establishes an
  interface contract. Facts about an interface — endpoint paths, field names,
  status codes, the error code a client expects on a cache miss — are
  interoperability facts, and recording them is the point of the exercise.
- **Never paste source from `hashicorp/packer` into this repository.** State
  the contract it implies and cite the file — that is what every entry in the
  compatibility reference does. Import `hcp-sdk-go` (MPL-2.0) directly rather
  than transliterating it.
- The dufflebag server does not bundle, embed, wrap, modify or redistribute
  Packer, and contains no Packer code; the end-to-end gates drive a separately
  installed stock `packer` binary as an external client. dufflebag is itself
  MPL-2.0.

This is a record of sources and working practice, not legal advice; it states
what the project did and what the licences say, and offers no conclusion
about how those licences apply.

## Tests are the deliverable, not a follow-up

- Unit tests include negative cases.
- Behaviour with a runtime surface gets integration, contract or e2e coverage
  — or an explicit written reason why not.
- A guard (authorization check, validation, CI gate) is done only when
  breaking it makes a named test fail. `make test-rls-sabotage` is the house
  pattern: CI proves the alarm rings.
- Cross-boundary test fixtures derive from the producing side or a captured
  real artifact — never write the other side's output from memory.

## Conventions

- **Standard tooling over bespoke code**: go-swagger, oapi-codegen, sqlc,
  golang-migrate, testcontainers. Replacing a standard tool needs explicit
  agreement first.
- **Comments are signposts, not exposition** — one or two lines stating what
  the code cannot show. Design history belongs in ADRs.
- **Generated code is never hand-edited.** `make generate-check` fails if
  generated trees drift from their inputs.
- **The compatibility plane is frozen.** Its handlers preserve client-observed
  behaviour, including its accidents; nothing extends it.

## Gates

`make help` lists everything. Before proposing a change:

```sh
make build test lint generate-check check-markers
make test-contract
make test-integration      # needs Docker
```

The browser smoke test (`make test-smoke`) and the real-client lanes
(`make test-e2e-terraform`, `make test-packer`) are load-bearing, not
optional extras — unit and contract suites agree with themselves, and only a
real client catches a seam where both sides are internally consistent.
