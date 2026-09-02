package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	osvQuerybatchLimit   = 1000
	osvDetailConcurrency = 4
	osvDatabaseRevision  = "unreported"
	osvRedHatEcosystem   = "Red Hat"
	osvEnterpriseLinux   = "enterprise_linux"
	// osvMaxResultPages bounds per-query pagination. OSV pages a single
	// query's vulnerability list when it exceeds the service's page size;
	// kernel-family source packages in whole-VM inventories legitimately
	// paginate (duf-1aon), but a token that never terminates must fail the
	// scan rather than loop.
	osvMaxResultPages = 100
)

// osvMaxResponseBytes is a variable only so the oversize guard is testable
// without a 32MiB fixture.
var osvMaxResponseBytes = 32 << 20

// OSV queries api.osv.dev (or a stand-in) for vulnerability findings. All
// HTTP goes through the configured client; the clock stamps attribution and
// probe observations so tests inject a fake.
type OSV struct {
	base   string
	client *http.Client
	clock  func() time.Time
}

func NewOSV(base string, client *http.Client, clock func() time.Time) *OSV {
	return &OSV{base: strings.TrimRight(base, "/"), client: client, clock: clock}
}

// Wire shapes. Only purl-derived Query fields ever enter these.
type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvQuery struct {
	Package   osvPackage `json:"package"`
	Version   string     `json:"version"`
	PageToken string     `json:"page_token,omitempty"`
}

type osvRef struct {
	ID string `json:"id"`
}

type osvBatchResult struct {
	Vulns         []osvRef `json:"vulns"`
	NextPageToken string   `json:"next_page_token"`
}

// osvBatchResponse decodes results as raw messages first: encoding/json maps
// a JSON null onto an untouched zero value, and a null result must fail the
// run rather than read as supported-with-zero-findings.
type osvBatchResponse struct {
	Results []json.RawMessage `json:"results"`
}

type osvQueryResponse struct {
	Vulns         []osvRef `json:"vulns"`
	NextPageToken string   `json:"next_page_token"`
}

type osvEvent struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}

type osvRange struct {
	Type   string     `json:"type"`
	Events []osvEvent `json:"events"`
}

type osvAffected struct {
	Package struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"package"`
	Ranges            []osvRange `json:"ranges"`
	EcosystemSpecific struct {
		Urgency string `json:"urgency"`
	} `json:"ecosystem_specific"`
}

type osvSeverity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

type osvRecord struct {
	ID               string        `json:"id"`
	Summary          string        `json:"summary"`
	Aliases          []string      `json:"aliases"`
	Related          []string      `json:"related"`
	Published        time.Time     `json:"published"`
	Modified         time.Time     `json:"modified"`
	Withdrawn        time.Time     `json:"withdrawn"`
	Severity         []osvSeverity `json:"severity"`
	Affected         []osvAffected `json:"affected"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
}

// submission pairs one inventory package with its translated query.
type submission struct {
	pkg Package
	q   *Query
}

// confirmationKey identifies one phase-2 stream-scoped query. One query
// answers every candidate advisory for that (ecosystem, name, version), so
// confirmations are deduplicated and transcript-ordered on this key.
type confirmationKey struct {
	ecosystem string
	name      string
	version   string
}

func (k confirmationKey) less(o confirmationKey) bool {
	if k.ecosystem != o.ecosystem {
		return k.ecosystem < o.ecosystem
	}
	if k.name != o.name {
		return k.name < o.name
	}
	return k.version < o.version
}

// scanState accumulates provider responses so the canonical transcript can be
// assembled on every exit path, error or not.
type scanState struct {
	mu            sync.Mutex
	chunks        [][]byte
	details       map[string][]byte
	confirmations map[confirmationKey][][]byte
}

func (s *scanState) addDetail(id string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.details[id] = body
}

// transcript assembles the canonical order: querybatch chunks by index, then
// detail responses by advisory id lexically, then phase-2 confirmation
// responses by (ecosystem, name, version) lexically.
func (s *scanState) transcript() Transcript {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Transcript
	t.Records = append(t.Records, s.chunks...)
	ids := make([]string, 0, len(s.details))
	for id := range s.details {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t.Records = append(t.Records, s.details[id])
	}
	keys := make([]confirmationKey, 0, len(s.confirmations))
	for k := range s.confirmations {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	for _, k := range keys {
		t.Records = append(t.Records, s.confirmations[k]...)
	}
	return t
}

func (o *OSV) Scan(ctx context.Context, inv Inventory) (Result, error) {
	res := Result{Attribution: Attribution{
		Adapter:          "osv",
		Engine:           o.base,
		DatabaseRevision: osvDatabaseRevision,
		ObservedAt:       o.clock(),
	}}
	state := &scanState{
		details:       map[string][]byte{},
		confirmations: map[confirmationKey][][]byte{},
	}
	findings, err := o.scan(ctx, inv, &res.Coverage, state)
	res.Transcript = state.transcript()
	if err != nil {
		return res, err
	}
	res.Findings = findings
	return res, nil
}

func (o *OSV) scan(ctx context.Context, inv Inventory, cov *Coverage, state *scanState) ([]Finding, error) {
	var subs []submission
	for _, p := range inv.Packages {
		class, q, _ := Classify(p.Purl)
		switch class {
		case ClassQueryable:
			cov.Submitted++
			subs = append(subs, submission{pkg: p, q: q})
		case ClassEmpty:
			cov.Empty++
		case ClassInvalid:
			cov.Invalid++
		case ClassUnversioned:
			cov.Unversioned++
		case ClassUnsupported:
			cov.Unsupported++
		}
	}

	candidates, err := o.querybatch(ctx, subs, state)
	if err != nil {
		return nil, err
	}

	if err := o.fetchDetails(ctx, candidates, state); err != nil {
		return nil, err
	}

	confirmed, err := o.confirmRedHat(ctx, subs, candidates, state)
	if err != nil {
		return nil, err
	}

	return o.buildFindings(subs, candidates, confirmed, state)
}

// querybatch submits every translated query in chunks and returns, per
// submission index, the candidate advisory ids.
func (o *OSV) querybatch(ctx context.Context, subs []submission, state *scanState) ([][]string, error) {
	candidates := make([][]string, len(subs))
	for start := 0; start < len(subs); start += osvQuerybatchLimit {
		end := min(start+osvQuerybatchLimit, len(subs))
		queries := make([]osvQuery, 0, end-start)
		for _, s := range subs[start:end] {
			queries = append(queries, osvQuery{
				Package: osvPackage{Name: s.q.Name, Ecosystem: s.q.Ecosystem},
				Version: s.q.Version,
			})
		}
		body, err := o.post(ctx, "/v1/querybatch", map[string]any{"queries": queries})
		if len(body) > 0 {
			state.chunks = append(state.chunks, body)
		}
		if err != nil {
			return nil, fmt.Errorf("querybatch chunk %d: %w", start/osvQuerybatchLimit, err)
		}
		var decoded osvBatchResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("querybatch chunk %d: decoding response: %w", start/osvQuerybatchLimit, err)
		}
		if len(decoded.Results) != len(queries) {
			return nil, fmt.Errorf("querybatch chunk %d: %d results for %d queries", start/osvQuerybatchLimit, len(decoded.Results), len(queries))
		}
		for i, raw := range decoded.Results {
			var r osvBatchResult
			if err := decodeStrict(raw, &r); err != nil {
				return nil, fmt.Errorf("querybatch chunk %d result %d: %w", start/osvQuerybatchLimit, i, err)
			}
			for _, v := range r.Vulns {
				candidates[start+i] = append(candidates[start+i], v.ID)
			}
			if r.NextPageToken != "" {
				more, err := o.querybatchPages(ctx, subs[start+i], r.NextPageToken, state)
				if err != nil {
					return nil, fmt.Errorf("querybatch chunk %d result %d: %w", start/osvQuerybatchLimit, i, err)
				}
				candidates[start+i] = append(candidates[start+i], more...)
			}
		}
	}
	return candidates, nil
}

// querybatchPages follows a result's pagination through single-query batch
// requests. Whole-VM inventories carry packages whose vulnerability lists
// exceed OSV's page size — kernel-family source packages paginate routinely —
// so continuation is part of the contract, bounded so a token that never
// terminates fails the scan instead of looping.
func (o *OSV) querybatchPages(ctx context.Context, sub submission, token string, state *scanState) ([]string, error) {
	var ids []string
	for page := 1; token != ""; page++ {
		if page > osvMaxResultPages {
			return nil, fmt.Errorf("pagination exceeded %d pages", osvMaxResultPages)
		}
		body, err := o.post(ctx, "/v1/querybatch", map[string]any{"queries": []osvQuery{{
			Package:   osvPackage{Name: sub.q.Name, Ecosystem: sub.q.Ecosystem},
			Version:   sub.q.Version,
			PageToken: token,
		}}})
		if len(body) > 0 {
			state.chunks = append(state.chunks, body)
		}
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		var decoded osvBatchResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("page %d: decoding response: %w", page, err)
		}
		if len(decoded.Results) != 1 {
			return nil, fmt.Errorf("page %d: %d results for 1 query", page, len(decoded.Results))
		}
		var r osvBatchResult
		if err := decodeStrict(decoded.Results[0], &r); err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		for _, v := range r.Vulns {
			ids = append(ids, v.ID)
		}
		token = r.NextPageToken
	}
	return ids, nil
}

// fetchDetails retrieves every distinct candidate advisory record through a
// fixed worker pool. Any failure fails the scan; bodies received so far stay
// in the transcript, though which in-flight fetches completed before the
// cancellation is inherently racy — a failed run's transcript is an audit
// artifact, not a stable value.
func (o *OSV) fetchDetails(ctx context.Context, candidates [][]string, state *scanState) error {
	distinct := map[string]bool{}
	for _, ids := range candidates {
		for _, id := range ids {
			distinct[id] = true
		}
	}
	ids := make([]string, 0, len(distinct))
	for id := range distinct {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for w := 0; w < osvDetailConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				body, err := o.get(ctx, "/v1/vulns/"+url.PathEscape(id))
				if len(body) > 0 {
					state.addDetail(id, body)
				}
				if err == nil {
					var record osvRecord
					if decodeErr := decodeStrict(body, &record); decodeErr != nil {
						err = decodeErr
					} else if record.ID != id {
						err = fmt.Errorf("record id %q does not map back to the requested advisory", record.ID)
					}
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("detail %s: %w", id, err)
						cancel()
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, id := range ids {
		select {
		case work <- id:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	return firstErr
}

// confirmRedHat runs the phase-2 stream-scoped confirmation queries. Phase-1
// "Red Hat" candidates are cross-stream noise until an exact ecosystem string
// from the record — same enterprise_linux major as the purl's distro — returns
// the advisory again (spike duf-o0ou.1). Returns, per confirmation key, the
// set of advisory ids the stream-scoped query confirmed.
func (o *OSV) confirmRedHat(ctx context.Context, subs []submission, candidates [][]string, state *scanState) (map[confirmationKey]map[string]bool, error) {
	needed := map[confirmationKey]bool{}
	for i, s := range subs {
		if s.q.Ecosystem != osvRedHatEcosystem {
			continue
		}
		for _, id := range candidates[i] {
			record, err := state.record(id)
			if err != nil {
				return nil, err
			}
			for _, eco := range redHatStreams(record, s.q) {
				needed[confirmationKey{ecosystem: eco, name: s.q.Name, version: s.q.Version}] = true
			}
		}
	}
	keys := make([]confirmationKey, 0, len(needed))
	for k := range needed {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })

	confirmed := map[confirmationKey]map[string]bool{}
	for _, k := range keys {
		ids := map[string]bool{}
		token := ""
		// page 0 is the initial request; continuations count 1..osvMaxResultPages,
		// matching querybatchPages' bound exactly.
		for page := 0; ; page++ {
			if page > osvMaxResultPages {
				return nil, fmt.Errorf("confirmation query %s/%s: pagination exceeded %d pages", k.ecosystem, k.name, osvMaxResultPages)
			}
			body, err := o.post(ctx, "/v1/query", osvQuery{
				Package:   osvPackage{Name: k.name, Ecosystem: k.ecosystem},
				Version:   k.version,
				PageToken: token,
			})
			if len(body) > 0 {
				state.confirmations[k] = append(state.confirmations[k], body)
			}
			if err != nil {
				return nil, fmt.Errorf("confirmation query %s/%s: %w", k.ecosystem, k.name, err)
			}
			var decoded osvQueryResponse
			if err := decodeStrict(body, &decoded); err != nil {
				return nil, fmt.Errorf("confirmation query %s/%s: %w", k.ecosystem, k.name, err)
			}
			for _, v := range decoded.Vulns {
				ids[v.ID] = true
			}
			token = decoded.NextPageToken
			if token == "" {
				break
			}
		}
		confirmed[k] = ids
	}
	return confirmed, nil
}

// record decodes a fetched detail body. The body was validated at fetch time,
// so absence here is an adapter invariant violation.
func (s *scanState) record(id string) (*osvRecord, error) {
	s.mu.Lock()
	body, ok := s.details[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("advisory %s: no fetched record", id)
	}
	var record osvRecord
	if err := decodeStrict(body, &record); err != nil {
		return nil, fmt.Errorf("advisory %s: %w", id, err)
	}
	return &record, nil
}

// redHatStreams lists the record's exact ecosystem strings that name the
// queried package in an enterprise_linux product (including suffixed variants
// like enterprise_linux_eus) of the purl's major, sorted for determinism.
func redHatStreams(record *osvRecord, q *Query) []string {
	seen := map[string]bool{}
	for _, a := range record.Affected {
		if a.Package.Name != q.Name {
			continue
		}
		if redHatStreamMajor(a.Package.Ecosystem) != q.RedHatMajor {
			continue
		}
		seen[a.Package.Ecosystem] = true
	}
	streams := make([]string, 0, len(seen))
	for eco := range seen {
		streams = append(streams, eco)
	}
	sort.Strings(streams)
	return streams
}

// matchesEcosystem compares a record's affected-entry ecosystem against the
// one we queried.
//
// Not equality, because Ubuntu accepts "Ubuntu:20.04" as a QUERY ecosystem
// while keying its affected entries "Ubuntu:20.04:LTS" — the support
// qualifier is not derivable from a purl (Syft reports ubuntu-20.04, and
// whether a release is LTS is not in the purl), so it can only be tolerated
// here. The suffix is stripped rather than prefix-matched so that
// "Ubuntu:Pro:20.04:LTS", a different product with its own support window,
// cannot be mistaken for the plain release.
func matchesEcosystem(affected, queried string) bool {
	return affected == queried || strings.TrimSuffix(affected, ":LTS") == queried
}

// redHatStreamMajor extracts the enterprise_linux major from a Red Hat
// ecosystem string ("Red Hat:enterprise_linux:8::baseos" -> "8",
// "Red Hat:enterprise_linux_eus:10.0" -> "10"); other products return "".
func redHatStreamMajor(ecosystem string) string {
	parts := strings.Split(ecosystem, ":")
	if len(parts) < 3 || parts[0] != osvRedHatEcosystem || !strings.HasPrefix(parts[1], osvEnterpriseLinux) {
		return ""
	}
	release := parts[2]
	if i := strings.IndexByte(release, '.'); i >= 0 {
		release = release[:i]
	}
	if _, err := strconv.Atoi(release); err != nil {
		return ""
	}
	return release
}

func (o *OSV) buildFindings(subs []submission, candidates [][]string, confirmed map[confirmationKey]map[string]bool, state *scanState) ([]Finding, error) {
	var findings []Finding
	for i, s := range subs {
		ids := append([]string(nil), candidates[i]...)
		sort.Strings(ids)
		for _, id := range ids {
			record, err := state.record(id)
			if err != nil {
				return nil, err
			}
			var matching []osvAffected
			if s.q.Ecosystem == osvRedHatEcosystem {
				for _, eco := range redHatStreams(record, s.q) {
					key := confirmationKey{ecosystem: eco, name: s.q.Name, version: s.q.Version}
					if confirmed[key][id] {
						for _, a := range record.Affected {
							if a.Package.Name == s.q.Name && a.Package.Ecosystem == eco {
								matching = append(matching, a)
							}
						}
					}
				}
				if len(matching) == 0 {
					// Cross-stream noise: no stream of this major confirmed
					// the advisory. A normal outcome, not an error.
					continue
				}
			} else {
				namesPackage := false
				for _, a := range record.Affected {
					if a.Package.Name != s.q.Name {
						continue
					}
					namesPackage = true
					if matchesEcosystem(a.Package.Ecosystem, s.q.Ecosystem) {
						matching = append(matching, a)
					}
				}
				if !namesPackage {
					// The record names no such package at all: the finding
					// cannot map back to the submitted identity, so the run
					// fails closed rather than attributing it to a guess.
					return nil, fmt.Errorf("advisory %s: no affected entry names %s", id, s.q.Name)
				}
				// matching may legitimately be empty: the provider matched OUR
				// package, but every range it carries belongs to another
				// stream. Ubuntu does this — a fix available only under Pro
				// leaves the plain release affected with nowhere to upgrade
				// to. The finding is real; projecting Pro's fixed version
				// would advertise a fix that standard support cannot install,
				// so it is reported with none.
			}
			findings = append(findings, buildFinding(s.pkg, record, matching))
		}
	}
	return findings, nil
}

func buildFinding(pkg Package, record *osvRecord, matching []osvAffected) Finding {
	fixedSet := map[string]bool{}
	for _, a := range matching {
		for _, r := range a.Ranges {
			// GIT ranges carry commit hashes, not versions. A forty-character
			// SHA cannot be acted on by someone deciding what to upgrade to,
			// and PYSEC and GHSA records routinely carry one alongside the
			// ECOSYSTEM range that holds the real answer. When a record has
			// ONLY a GIT range the fix is not in a release yet, and no fixed
			// version is the honest projection.
			if strings.EqualFold(r.Type, "GIT") {
				continue
			}
			for _, e := range r.Events {
				if e.Fixed != "" {
					fixedSet[e.Fixed] = true
				}
			}
		}
	}
	fixed := make([]string, 0, len(fixedSet))
	for v := range fixedSet {
		fixed = append(fixed, v)
	}
	sort.Strings(fixed)

	var severities []SeverityValue
	for _, sv := range record.Severity {
		severities = append(severities, SeverityValue{Source: "osv", Type: sv.Type, Value: sv.Score})
	}
	if record.DatabaseSpecific.Severity != "" {
		severities = append(severities, SeverityValue{Source: "osv:database_specific", Type: "label", Value: record.DatabaseSpecific.Severity})
	}
	urgencies := map[string]bool{}
	for _, a := range matching {
		if u := a.EcosystemSpecific.Urgency; u != "" && !urgencies[u] {
			urgencies[u] = true
			severities = append(severities, SeverityValue{Source: "osv:ecosystem_specific", Type: "urgency", Value: u})
		}
	}

	return Finding{
		Package:       pkg,
		ID:            record.ID,
		Summary:       record.Summary,
		Aliases:       record.Aliases,
		Related:       record.Related,
		Published:     record.Published,
		Modified:      record.Modified,
		Withdrawn:     record.Withdrawn,
		FixedVersions: fixed,
		Severities:    severities,
		Severity:      deriveSeverity(severities),
	}
}

// Probe answers whether the provider serves the batch endpoint: an empty
// querybatch exercises the real POST path at minimal cost.
func (o *OSV) Probe(ctx context.Context) (Health, error) {
	started := o.clock()
	_, err := o.post(ctx, "/v1/querybatch", map[string]any{"queries": []osvQuery{}})
	health := Health{
		OK:         err == nil,
		Latency:    o.clock().Sub(started),
		ObservedAt: started,
	}
	if err != nil {
		health.Detail = err.Error()
		return health, err
	}
	return health, nil
}

func (o *OSV) post(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return o.do(req)
}

func (o *OSV) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+path, nil)
	if err != nil {
		return nil, err
	}
	return o.do(req)
}

// do returns the response body alongside any error: a failed request's body
// was still received, and the transcript contract wants it retained.
func (o *OSV) do(req *http.Request) ([]byte, error) {
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(osvMaxResponseBytes)+1))
	if err != nil {
		return body, err
	}
	if len(body) > osvMaxResponseBytes {
		// A truncated transcript record would misrepresent what the provider
		// sent; oversize is a hard failure, not a silent cut.
		return body[:osvMaxResponseBytes], fmt.Errorf("%s %s: response exceeds %d bytes", req.Method, req.URL.Path, osvMaxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL.Path, resp.StatusCode, truncate(body, 200))
	}
	return body, nil
}

// decodeStrict rejects JSON null bodies and entries: encoding/json leaves the
// target untouched for null, which would read as a clean result.
func decodeStrict(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 || string(bytes.TrimSpace(body)) == "null" {
		return fmt.Errorf("decoding response: null or empty body")
	}
	return json.Unmarshal(body, v)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
