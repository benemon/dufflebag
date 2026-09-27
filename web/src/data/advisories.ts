import { SEVERITY_ORDER, type Severity } from './findings'
import type { Finding, Package } from './versions'

export type AdvisoryHit = {
  packageIdentity: string
  name: string
  version: string
  sbomID: string
  fixedVersion: string
}

export type Advisory = {
  identifier: string
  severity: Severity
  hits: AdvisoryHit[]
  /** Distinct package identities among the hits; a package in two SBOMs counts once. */
  packages: number
  aliases: string[]
  published: string
}

export type AdvisoryCounts = Record<Severity, number>

export type VulnerablePackage = {
  identity: string
  name: string
  version: string
  critical: number
  high: number
}

export type BuildAdvisories = {
  advisories: Advisory[]
  counts: AdvisoryCounts
  affectedPackages: number
  topVulnerablePackages: VulnerablePackage[]
}

export function packageIdentity(pkg: Pick<Package, 'purl' | 'name' | 'version'>): string {
  return pkg.purl || `${pkg.name}@${pkg.version}`
}

function severityRank(value: string): number {
  const rank = SEVERITY_ORDER.indexOf(value.toLowerCase() as Severity)
  return rank < 0 ? 0 : rank
}

function severity(value: string): Severity {
  const normalized = value.toLowerCase() as Severity
  return SEVERITY_ORDER.includes(normalized) ? normalized : 'unknown'
}

function newest(left: string, right: string): string {
  if (!left) return right
  if (!right) return left
  return Date.parse(right) > Date.parse(left) ? right : left
}

function hitsForFinding(pkg: Package, finding: Finding): AdvisoryHit[] {
  const identity = packageIdentity(pkg)
  const sboms = pkg.sboms.length > 0 ? pkg.sboms : [{ id: '', name: '', format: '' }]
  return sboms.map((sbom) => ({
    packageIdentity: identity,
    name: pkg.name,
    version: pkg.version,
    sbomID: sbom.id || sbom.name || sbom.format,
    fixedVersion: finding.fixedVersion,
  }))
}

/** One build's package findings projected into distinct, sortable advisories. */
export function deriveBuildAdvisories(packages: Package[]): BuildAdvisories {
  const byIdentifier = new Map<string, Advisory>()
  for (const pkg of packages) {
    for (const finding of pkg.findings ?? []) {
      const band = severity(finding.criticality)
      const existing = byIdentifier.get(finding.identifier)
      if (!existing) {
        byIdentifier.set(finding.identifier, {
          identifier: finding.identifier,
          severity: band,
          hits: hitsForFinding(pkg, finding),
          packages: 0,
          aliases: [...new Set(finding.aliases)],
          published: finding.published,
        })
        continue
      }
      if (severityRank(band) > severityRank(existing.severity)) existing.severity = band
      existing.aliases = [...new Set([...existing.aliases, ...finding.aliases])]
      existing.published = newest(existing.published, finding.published)
      const known = new Set(existing.hits.map((hit) => [
        hit.packageIdentity, hit.sbomID, hit.fixedVersion,
      ].join('\u0000')))
      for (const hit of hitsForFinding(pkg, finding)) {
        const key = [hit.packageIdentity, hit.sbomID, hit.fixedVersion].join('\u0000')
        if (!known.has(key)) {
          existing.hits.push(hit)
          known.add(key)
        }
      }
    }
  }

  const counts = Object.fromEntries(
    SEVERITY_ORDER.map((band) => [band, 0]),
  ) as AdvisoryCounts
  const packagesByIdentity = new Map<string, VulnerablePackage>()
  const affected = new Set<string>()
  for (const advisory of byIdentifier.values()) {
    counts[advisory.severity] += 1
    const advisoryPackages = new Map<string, AdvisoryHit>()
    for (const hit of advisory.hits) advisoryPackages.set(hit.packageIdentity, hit)
    advisory.packages = advisoryPackages.size
    for (const hit of advisoryPackages.values()) {
      affected.add(hit.packageIdentity)
      if (advisory.severity !== 'critical' && advisory.severity !== 'high') continue
      const current = packagesByIdentity.get(hit.packageIdentity) ?? {
        identity: hit.packageIdentity,
        name: hit.name,
        version: hit.version,
        critical: 0,
        high: 0,
      }
      current[advisory.severity] += 1
      packagesByIdentity.set(hit.packageIdentity, current)
    }
  }

  const advisories = [...byIdentifier.values()].sort((left, right) =>
    severityRank(right.severity) - severityRank(left.severity) ||
    right.packages - left.packages ||
    Date.parse(right.published || '1970-01-01') - Date.parse(left.published || '1970-01-01'),
  )
  const topVulnerablePackages = [...packagesByIdentity.values()]
    .sort((left, right) =>
      right.critical - left.critical || right.high - left.high ||
      left.name.localeCompare(right.name),
    )
    .slice(0, 5)

  return { advisories, counts, affectedPackages: affected.size, topVulnerablePackages }
}
