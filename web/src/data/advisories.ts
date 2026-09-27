import type { ApiBuildFindingsResponse } from '../api/client'
import { SEVERITY_ORDER, type Severity } from './findings'
import type { Package } from './versions'

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

export type BuildFindingsData = {
  scannerConfigured: boolean
  inventory: 'parsed' | 'unparseable'
  packagesTotal: number
  scanned: boolean
  run: { id: string; observedAt: string } | null
  packagesAffected: number
  advisories: Advisory[]
  counts: AdvisoryCounts
  topVulnerablePackages: VulnerablePackage[]
}

export function packageIdentity(pkg: Pick<Package, 'purl' | 'name' | 'version'>): string {
  return pkg.purl || `${pkg.name}@${pkg.version}`
}

function severity(value: string): Severity {
  const normalized = value.toLowerCase() as Severity
  return SEVERITY_ORDER.includes(normalized) ? normalized : 'unknown'
}

/** The server orders and deduplicates; the client only reshapes for the screen. */
export function projectBuildFindings(response: ApiBuildFindingsResponse): BuildFindingsData {
  const advisories = response.advisories.map((advisory) => {
    const hits = advisory.packages.map((pkg) => ({
      packageIdentity: packageIdentity(pkg),
      name: pkg.name,
      version: pkg.version,
      sbomID: pkg.sbom_id,
      fixedVersion: pkg.fixed_version,
    }))
    return {
      identifier: advisory.identifier,
      severity: severity(advisory.severity),
      hits,
      packages: new Set(hits.map((hit) => hit.packageIdentity)).size,
      aliases: advisory.aliases,
      published: advisory.published ?? '',
    }
  })
  const counts = Object.fromEntries(SEVERITY_ORDER.map((band) => [band, 0])) as AdvisoryCounts
  const packagesByIdentity = new Map<string, VulnerablePackage>()
  for (const advisory of advisories) {
    counts[advisory.severity] += 1
    if (advisory.severity !== 'critical' && advisory.severity !== 'high') continue
    const seen = new Set<string>()
    for (const hit of advisory.hits) {
      if (seen.has(hit.packageIdentity)) continue
      seen.add(hit.packageIdentity)
      const current = packagesByIdentity.get(hit.packageIdentity) ?? {
        identity: hit.packageIdentity, name: hit.name, version: hit.version, critical: 0, high: 0,
      }
      current[advisory.severity] += 1
      packagesByIdentity.set(hit.packageIdentity, current)
    }
  }
  const topVulnerablePackages = [...packagesByIdentity.values()]
    .sort((left, right) =>
      right.critical - left.critical || right.high - left.high ||
      left.name.localeCompare(right.name),
    )
    .slice(0, 5)
  return {
    scannerConfigured: response.scanner_configured,
    inventory: response.inventory,
    packagesTotal: response.packages_total,
    scanned: response.scanned,
    run: response.run ? { id: response.run.id, observedAt: response.run.observed_at } : null,
    packagesAffected: response.packages_affected,
    advisories,
    counts,
    topVulnerablePackages,
  }
}
