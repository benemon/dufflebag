import { Card, CardBody, CardTitle, Content, Label } from '@patternfly/react-core'

import {
  coverageSummary, hasCoverageGap, versionRollup, type BuildFindings,
} from '../data/findings'
import { OUT_OF_SCAN_SET_CLASS } from './Findings'

const SEVERITY_COLOUR: Record<string, 'red' | 'orange' | 'yellow' | 'blue' | 'grey'> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'blue',
  negligible: 'grey', unknown: 'grey',
}

/**
 * A version's security answer, which is what a reader arriving from a channel
 * came to find.
 *
 * The headline counts each finding once for the version; the per-build list
 * below says where each lives. Summing the builds instead would report one
 * flaw shipped on three platforms as three problems.
 */
export function VersionSecurityCard({
  builds, onOpenBuild, outOfScanSet,
}: {
  builds: BuildFindings[]
  /** Each build tile opens that build on this version. */
  onOpenBuild: (buildID: string) => void
  /** No channel selects this version, so the figures are no longer maintained. */
  outOfScanSet: boolean
}) {
  const scanned = builds.filter((build) => build.scan)
  if (scanned.length === 0) {
    return (
      <Card>
        <CardTitle>Security</CardTitle>
        <CardBody>
          <Content component="p" data-state="never-scanned">
            Not scanned. No vulnerability source is configured for this deployment.
          </Content>
        </CardBody>
      </Card>
    )
  }

  const summary = versionRollup(builds)
  const attribution = scanned[0]?.scan
  // Coverage appears ONLY when something was not examined. With full coverage
  // the counts are noise; with a gap they are the difference between "nothing
  // found" and "not looked at", which is the distinction the console exists to
  // preserve.
  const coverage = hasCoverageGap(attribution) ? coverageSummary(attribution) : []
  const lastScanned = attribution?.observedAt
    ? new Date(attribution.observedAt).toISOString().slice(0, 10)
    : null

  return (
    <Card className={outOfScanSet ? OUT_OF_SCAN_SET_CLASS : undefined}>
      <CardTitle>
        <span style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12 }}>
          <span>Security</span>
          <Content component="small">
            {lastScanned ? `Last scanned: ${lastScanned}` : 'Not yet scanned'}
          </Content>
        </span>
      </CardTitle>
      <CardBody>
        <div
          style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}
          data-state={summary.worst ? 'findings' : 'zero-findings'}
        >
          <Label color={summary.worst ? SEVERITY_COLOUR[summary.worst] ?? 'grey' : 'grey'}>
            {summary.worst ?? 'No known findings'}
          </Label>
          {summary.worst && (
            <Content component="p" style={{ margin: 0 }}>
              {summary.findings} {summary.findings === 1 ? 'finding' : 'findings'} across{' '}
              {summary.affectedPackages}{' '}
              {summary.affectedPackages === 1 ? 'package' : 'packages'}
            </Content>
          )}
        </div>

        {summary.counts.length > 0 && (
          <div style={{ marginTop: 12, display: 'flex', gap: 7, flexWrap: 'wrap' }}>
            {summary.counts.map(({ severity, count }) => (
              <Label key={severity} color={SEVERITY_COLOUR[severity] ?? 'grey'} isCompact>
                {count} {severity}
              </Label>
            ))}
          </div>
        )}

        {outOfScanSet && (
          <Content component="p" style={{ marginTop: 12, color: '#4d4d4d' }}>
            No channel selects this version, so these figures are not being updated.
          </Content>
        )}
        {coverage.length > 0 && (
          <Content component="p" style={{ color: '#4d4d4d' }} data-coverage="true">
            Coverage: {coverage.join('; ')}.
          </Content>
        )}

        <div style={{ marginTop: 20 }}>
          <Content component="small" style={{ textTransform: 'uppercase', letterSpacing: '.04em' }}>
            By build
          </Content>
          <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
            {summary.builds.map((build) => (
              <button
                key={build.buildID}
                type="button"
                onClick={() => onOpenBuild(build.buildID)}
                style={{
                  font: 'inherit', textAlign: 'left', cursor: 'pointer', background: 'none',
                  width: '100%',
                  display: 'flex', alignItems: 'center', gap: 14, padding: '11px 14px',
                  border: '1px solid #e0e0e0', borderRadius: 3, textDecoration: 'none',
                  color: 'inherit',
                }}
                data-build-link={build.buildID}
              >
                <span style={{ flex: '0 1 auto', minWidth: 0 }}>
                  <span style={{ display: 'block', fontWeight: 500 }}>{build.platform}</span>
                  <code
                    style={{
                      display: 'block', fontSize: 12, color: '#4d4d4d',
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }}
                    title={build.component || build.buildID}
                  >
                    {build.component || build.buildID}
                  </code>
                </span>
                <span style={{ flex: 'none' }}>
                  <Label color={build.worst ? SEVERITY_COLOUR[build.worst] ?? 'grey' : 'grey'} isCompact>
                    {/* Never "clean": a build with nothing found is reported as
                        an absence of findings, not as a verdict of safety. */}
                    {build.worst ?? 'no findings'}
                  </Label>
                </span>
                <span
                  style={{
                    flex: '0 1 auto', marginLeft: 'auto', display: 'flex', gap: 6,
                    flexWrap: 'wrap', justifyContent: 'flex-end',
                  }}
                >
                  {build.counts.length > 0 ? (
                    build.counts.map(({ severity, count }) => (
                      <Label key={severity} color={SEVERITY_COLOUR[severity] ?? 'grey'} isCompact variant="outline">
                        {count} {severity}
                      </Label>
                    ))
                  ) : (
                    <Content component="small">{build.scanned} scanned</Content>
                  )}
                </span>
                <span style={{ flex: 'none', color: '#4d4d4d' }}>›</span>
              </button>
            ))}
          </div>
        </div>
      </CardBody>
    </Card>
  )
}
