/**
 * Severity rollup derivation.
 *
 * Kept apart from the screens because the counting rule is the part that can
 * be wrong in ways nobody notices: it is a property of the data, so it is
 * tested as one.
 */

/** The fixed display scale, worst last. */
export const SEVERITY_ORDER = [
  'unknown',
  'negligible',
  'low',
  'medium',
  'high',
  'critical',
] as const

export type Severity = (typeof SEVERITY_ORDER)[number]

/**
 * The PROJECTED shape the screens hold, not the wire shape. The rollup counts
 * what is displayed, so it speaks the same vocabulary the table does.
 */
export type PackageRow = {
  name?: string
  version?: string
  purl?: string
  findings?: { criticality?: string; identifier?: string }[]
}

/**
 * Attribution accompanying a scanned response, from the Dufflebag-Scan-*
 * headers. Absent entirely when no scanner is configured — which is why every
 * field is optional and the whole object may be undefined.
 */
export type ScanAttribution = {
  adapter?: string
  engine?: string
  databaseRevision?: string
  observedAt?: string
  submitted?: number
  invalid?: number
  unversioned?: number
  unsupported?: number
}

export type Rollup = {
  /** The worst severity present, or undefined when nothing was found. */
  worst?: Severity
  /**
   * Distinct affected package instances at the worst severity. NOT a count of
   * findings: two advisories against one package is one affected package.
   */
  affectedAtWorst: number
  /** Distinct affected package instances at any severity. */
  affectedTotal: number
}

function rank(severity: string | undefined): number {
  const index = SEVERITY_ORDER.indexOf((severity ?? 'unknown') as Severity)
  return index < 0 ? 0 : index
}

/**
 * The identity a package instance is counted by. Deliberately includes the
 * build: the same package in two builds is two affected instances, because
 * two images are affected. It deliberately excludes the SBOM, so a package
 * reported by both an application and a base SBOM of ONE build counts once.
 */
function instanceKey(buildID: string, pkg: PackageRow): string {
  return [buildID, pkg.name ?? '', pkg.version ?? '', pkg.purl ?? ''].join(' ')
}

/** The worst severity a package instance carries, or undefined if unaffected. */
function worstForPackage(pkg: PackageRow): Severity | undefined {
  let worst: Severity | undefined
  for (const finding of pkg.findings ?? []) {
    // criticality is the derived fixed scale; the provider's verbatim
    // severity is NOT comparable across providers and is display-only.
    const band = (finding.criticality ?? 'unknown').toLowerCase() as Severity
    if (!worst || rank(band) > rank(worst)) {
      worst = band
    }
  }
  return worst
}

/**
 * Roll a build's packages up to one severity and a count.
 *
 * Accepts several builds so a version aligned to a channel can roll up every
 * build beneath it; pass one entry for a single build.
 */
export function rollup(builds: { buildID: string; packages: PackageRow[] }[]): Rollup {
  const worstByInstance = new Map<string, Severity>()
  for (const build of builds) {
    for (const pkg of build.packages) {
      const band = worstForPackage(pkg)
      if (!band) continue
      const key = instanceKey(build.buildID, pkg)
      const existing = worstByInstance.get(key)
      if (!existing || rank(band) > rank(existing)) {
        worstByInstance.set(key, band)
      }
    }
  }
  if (worstByInstance.size === 0) {
    return { affectedAtWorst: 0, affectedTotal: 0 }
  }
  let worst: Severity = 'unknown'
  for (const band of worstByInstance.values()) {
    if (rank(band) > rank(worst)) worst = band
  }
  let affectedAtWorst = 0
  for (const band of worstByInstance.values()) {
    if (band === worst) affectedAtWorst += 1
  }
  return { worst, affectedAtWorst, affectedTotal: worstByInstance.size }
}

/**
 * Coverage states are reported separately from findings because OSV answers an
 * ecosystem it does not cover exactly as it answers one it covers and found
 * nothing in. Only the adapter's own classification distinguishes them, so the
 * console must show it rather than implying everything was examined.
 */
/** One severity band with how many findings sit at it. */
export type SeverityCount = { severity: Severity; count: number }

/**
 * Findings grouped by band, worst first. The table cell shows these instead of
 * prose: the column then has a fixed width and cannot grow with content, which
 * is what made the previous rendering wrap off the screen.
 */
export function severityCounts(findings: { criticality?: string }[]): SeverityCount[] {
  const counts = new Map<Severity, number>()
  for (const finding of findings) {
    const band = (finding.criticality ?? 'unknown').toLowerCase() as Severity
    counts.set(band, (counts.get(band) ?? 0) + 1)
  }
  return [...SEVERITY_ORDER]
    .reverse()
    .filter((band) => counts.has(band))
    .map((band) => ({ severity: band, count: counts.get(band) as number }))
}

export function coverageSummary(attribution: ScanAttribution | undefined): string[] {
  if (!attribution) return []
  const parts: string[] = []
  if (attribution.submitted !== undefined) {
    parts.push(`${attribution.submitted} queried`)
  }
  if (attribution.unsupported) {
    parts.push(`${attribution.unsupported} in ecosystems the scanner does not cover`)
  }
  if (attribution.unversioned) {
    parts.push(`${attribution.unversioned} without a version to match`)
  }
  if (attribution.invalid) {
    parts.push(`${attribution.invalid} with an unreadable identifier`)
  }
  return parts
}

/** True when any package was not examined, whatever the finding count. */
export function hasCoverageGap(attribution: ScanAttribution | undefined): boolean {
  if (!attribution) return false
  return Boolean(attribution.unsupported || attribution.unversioned || attribution.invalid)
}

/** Parses the Dufflebag-Scan-* headers into attribution, or undefined. */
export function scanAttribution(headers: Headers): ScanAttribution | undefined {
  const adapter = headers.get('Dufflebag-Scan-Adapter')
  if (!adapter) return undefined
  const number = (name: string): number | undefined => {
    const raw = headers.get(name)
    if (raw === null) return undefined
    const value = Number(raw)
    return Number.isFinite(value) ? value : undefined
  }
  return {
    adapter,
    engine: headers.get('Dufflebag-Scan-Engine') ?? undefined,
    databaseRevision: headers.get('Dufflebag-Scan-Database-Revision') ?? undefined,
    observedAt: headers.get('Dufflebag-Scan-Observed-At') ?? undefined,
    submitted: number('Dufflebag-Scan-Submitted'),
    invalid: number('Dufflebag-Scan-Invalid'),
    unversioned: number('Dufflebag-Scan-Unversioned'),
    unsupported: number('Dufflebag-Scan-Unsupported'),
  }
}

/** One build's contribution to a version's rollup. */
export type BuildFindings = {
  buildID: string
  platform: string
  /** The Packer build name, e.g. docker.distro — what the author called it. */
  component: string
  packages: PackageRow[]
  scan?: ScanAttribution
  /** Packages examined, for a build that has findings-free coverage to report. */
  scanned: number
}

export type BuildRollup = {
  buildID: string
  platform: string
  component: string
  scanned: number
  worst?: Severity
  counts: SeverityCount[]
}

export type VersionRollup = {
  worst?: Severity
  /** Distinct (advisory, package) pairs, deduplicated ACROSS builds. */
  counts: SeverityCount[]
  findings: number
  affectedPackages: number
  builds: BuildRollup[]
}

/**
 * A version's findings, counted once each.
 *
 * Summing the builds would double-count everything present on every platform:
 * one flaw in libcurl shipped on docker, aws and azure is ONE problem in three
 * places, not three problems. The version headline therefore deduplicates by
 * (advisory, package) and the per-build breakdown says where each one lives.
 */
export function versionRollup(builds: BuildFindings[]): VersionRollup {
  const worstByFinding = new Map<string, Severity>()
  const affected = new Set<string>()

  for (const build of builds) {
    for (const pkg of build.packages) {
      for (const finding of pkg.findings ?? []) {
        const band = (finding.criticality ?? 'unknown').toLowerCase() as Severity
        // Deliberately NOT keyed by build: that is the deduplication.
        const key = [pkg.name ?? '', pkg.version ?? '', pkg.purl ?? '', finding.identifier ?? ''].join(' ')
        const existing = worstByFinding.get(key)
        if (!existing || rank(band) > rank(existing)) worstByFinding.set(key, band)
        affected.add([pkg.name ?? '', pkg.version ?? '', pkg.purl ?? ''].join(' '))
      }
    }
  }

  return {
    worst: worstOf([...worstByFinding.values()]),
    counts: tally([...worstByFinding.values()]),
    findings: worstByFinding.size,
    affectedPackages: affected.size,
    builds: builds.map((build) => {
      const bands: Severity[] = []
      for (const pkg of build.packages) {
        for (const finding of pkg.findings ?? []) {
          bands.push((finding.criticality ?? 'unknown').toLowerCase() as Severity)
        }
      }
      return {
        buildID: build.buildID,
        platform: build.platform,
        component: build.component,
        scanned: build.scanned,
        worst: worstOf(bands),
        counts: tally(bands),
      }
    }),
  }
}

function worstOf(bands: Severity[]): Severity | undefined {
  let worst: Severity | undefined
  for (const band of bands) {
    if (!worst || rank(band) > rank(worst)) worst = band
  }
  return worst
}

function tally(bands: Severity[]): SeverityCount[] {
  const counts = new Map<Severity, number>()
  for (const band of bands) counts.set(band, (counts.get(band) ?? 0) + 1)
  return [...SEVERITY_ORDER].reverse()
    .filter((band) => counts.has(band))
    .map((band) => ({ severity: band, count: counts.get(band) as number }))
}

/** A finding identified per package, used to key deduplication. */
