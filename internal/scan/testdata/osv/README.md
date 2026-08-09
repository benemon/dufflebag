# OSV.dev fixture captures

Live api.osv.dev responses captured 2026-08-06 with `curl -sS -f` during the
duf-o0ou.1/duf-o0ou.2 work, replayed through httptest by the adapter tests —
CI never touches the live API. `querybatch-*.json` are POST /v1/querybatch
responses for the spike manifest's control queries, `detail-*.json` are
GET /v1/vulns/{id} records, `query-redhat-baseos-*.json` are the phase-2
stream-scoped POST /v1/query responses. Synthesized edge-case bodies
(cardinality, pagination, identity mismatch, multi-candidate determinism) are
built in the tests by minimally mutating these captures; the mutation is
visible at each use site.

detail-UBUNTU-CVE-2026-42250.json was captured 2026-08-07 while diagnosing a
demo failure: OSV accepts `Ubuntu:20.04` as a query ecosystem but keys the
advisory's affected entries `Ubuntu:20.04:LTS`, which the strict
map-back-to-the-queried-identity check rejected. It pins that shape.
