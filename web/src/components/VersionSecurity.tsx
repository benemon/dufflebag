import {
  Card, CardBody, CardTitle, Content, DataList, DataListCell, DataListItem,
  DataListItemCells, DataListItemRow, Label, Title, Truncate,
} from '@patternfly/react-core'
import AngleRightIcon from '@patternfly/react-icons/dist/esm/icons/angle-right-icon'

import { coverageSummary, hasCoverageGap } from '../data/findings'
import type { VersionSecuritySummary } from '../data/versions'
import { OUT_OF_SCAN_SET_CLASS, SEVERITY_COLOUR } from './Findings'
import { When } from './When'

/**
 * A version's security answer, which is what a reader arriving from a channel
 * came to find.
 *
 * The headline counts each finding once for the version; the per-build list
 * below says where each lives. Summing the builds instead would report one
 * flaw shipped on three platforms as three problems.
 */
export function VersionSecurityCard({
  summary, onOpenBuild, outOfScanSet,
}: {
  summary: VersionSecuritySummary
  /** Each build tile opens that build on this version. */
  onOpenBuild: (buildID: string) => void
  /** No channel selects this version, so the figures are no longer maintained. */
  outOfScanSet: boolean
}) {
  if (!summary.version) {
    const scannerConfigured = summary.scannerConfigured
    // A build scanned before summaries were stored has findings but no
    // figures here yet; calling it unscanned would be false.
    const scanned = summary.builds.some((build) => build.scanned)
    const state = !scannerConfigured ? 'never-scanned' : scanned ? 'scanned-no-summary' : 'not-yet-scanned'
    return (
      <Card>
        <CardTitle>Security</CardTitle>
        <CardBody>
          <Content component="p" data-state={state}>
            {state === 'never-scanned'
              ? 'Not scanned. No vulnerability source is configured for this deployment.'
              : state === 'scanned-no-summary'
                ? 'Scanned. Figures appear here after the next scan of this version\'s builds.'
                : 'Not yet scanned. Findings appear once the scanner has examined a build of this version.'}
          </Content>
        </CardBody>
      </Card>
    )
  }

  const version = summary.version
  // Coverage appears ONLY when something was not examined, and per build:
  // summed across builds, one package shipped on three platforms would count
  // three times. With a gap the counts are the difference between "nothing
  // found" and "not looked at", which is the distinction the console exists to
  // preserve.
  const coverageGaps = summary.builds.filter((build) => hasCoverageGap(build.summary?.scan))
  // Compared as instants: RFC 3339 strings with and without fractional seconds
  // do not sort lexically.
  const failedRescans = summary.builds.filter((build) => build.latestAttempt?.status === 'failed')
  const lastFailure = failedRescans.reduce<string | undefined>((latest, build) => {
    const observed = build.latestAttempt?.observedAt
    return observed && (!latest || Date.parse(observed) > Date.parse(latest)) ? observed : latest
  }, undefined)
  const unsummarised = summary.builds.filter((build) => !build.summary && build.inventory !== 'unparseable')
  const pending = unsummarised.filter((build) => build.scanned).length
  const unscanned = unsummarised.length - pending
  const lastScanned = summary.builds.reduce<string | undefined>((latest, build) => {
    const observed = build.summary?.observedAt
    return observed && (!latest || Date.parse(observed) > Date.parse(latest)) ? observed : latest
  }, undefined)

  return (
    <Card className={outOfScanSet ? OUT_OF_SCAN_SET_CLASS : undefined}>
      <CardTitle>
        <span style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12 }}>
          <span>Security</span>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            {outOfScanSet ? <Label color="grey" variant="outline">not updated</Label> : null}
            <Content component="small">
              {lastScanned ? <>Last scanned: <When iso={lastScanned} dateOnly /></> : 'Not yet scanned'}
            </Content>
          </span>
        </span>
      </CardTitle>
      <CardBody>
        <div
          style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}
          data-state={version.worst ? 'findings' : 'zero-findings'}
        >
          <Label color={version.worst ? SEVERITY_COLOUR[version.worst] ?? 'grey' : 'grey'}>
            {version.worst ?? 'No known findings'}
          </Label>
          {version.worst && (
            <Content component="p" style={{ margin: 0 }}>
              {version.findings} {version.findings === 1 ? 'finding' : 'findings'} across{' '}
              {version.affectedPackages}{' '}
              {version.affectedPackages === 1 ? 'package' : 'packages'}
            </Content>
          )}
        </div>

        {version.counts.length > 0 && (
          <div style={{ marginTop: 12, display: 'flex', gap: 7, flexWrap: 'wrap' }}>
            {version.counts.map(({ severity, count }) => (
              <Label key={severity} color={SEVERITY_COLOUR[severity] ?? 'grey'} isCompact>
                {count} {severity}
              </Label>
            ))}
          </div>
        )}

        {failedRescans.length > 0 && (
          <Content component="p" style={{ marginTop: 12 }} data-rescan="failed">
            The latest rescan failed for {failedRescans.length}{' '}
            {failedRescans.length === 1 ? 'build' : 'builds'}
            {lastFailure ? <> on <When iso={lastFailure} dateOnly /></> : null}; the figures are from
            the last successful scan.
          </Content>
        )}
        {version.buildsSummarised < summary.builds.length && (
          <Content component="p" style={{ marginTop: 12, color: 'var(--pf-t--global--text--color--subtle)' }}>
            Covers {version.buildsSummarised} of {summary.builds.length} builds;{' '}
            {pending === 0
              ? 'the rest are not yet scanned.'
              : [
                `${pending} scanned ${pending === 1 ? 'build shows its' : 'builds show their'} figures after the next scan`,
                ...(unscanned > 0 ? [`${unscanned} not yet scanned`] : []),
              ].join('; ') + '.'}
          </Content>
        )}
        {outOfScanSet && (
          <Content component="p" style={{ marginTop: 12, color: 'var(--pf-t--global--text--color--subtle)' }}>
            No channel selects this version, so these figures are not being updated.
          </Content>
        )}
        {coverageGaps.map((build) => (
          <Content key={build.buildID} component="p" style={{ color: 'var(--pf-t--global--text--color--subtle)' }} data-coverage="true">
            Coverage on {build.platform}: {coverageSummary(build.summary?.scan).join('; ')}.
          </Content>
        ))}

        <div style={{ marginTop: 20 }}>
          <Title headingLevel="h3" size="md">
            By build
          </Title>
          <DataList
            aria-label="Security by build"
            onSelectDataListItem={(_event, buildID) => onOpenBuild(buildID)}
            style={{ marginTop: 8 }}
          >
            {summary.builds.map((build) => {
              const buildSummary = build.summary
              return (
                <DataListItem
                  key={build.buildID}
                  id={build.buildID}
                  aria-labelledby={`security-build-${build.buildID}`}
                  data-build-link={build.buildID}
                >
                  <DataListItemRow>
                    <DataListItemCells dataListCells={[
                      <DataListCell key="build">
                        <span id={`security-build-${build.buildID}`} style={{ display: 'block', fontWeight: 500 }}>
                          {build.platform}
                        </span>
                        <code style={{ display: 'block', color: 'var(--pf-t--global--text--color--subtle)' }}>
                          <Truncate content={build.component || build.buildID} />
                        </code>
                      </DataListCell>,
                      <DataListCell key="severity">
                        <Label color={buildSummary?.worst ? SEVERITY_COLOUR[buildSummary.worst] ?? 'grey' : 'grey'} isCompact>
                          {build.inventory === 'unparseable'
                            ? 'SBOM unparseable'
                            : buildSummary?.worst ?? (buildSummary ? 'no findings' : build.scanned ? 'summary pending' : 'not scanned')}
                        </Label>
                        {build.latestAttempt?.status === 'failed' ? (
                          <Label color="orange" variant="outline" isCompact style={{ marginLeft: 6 }}>rescan failed</Label>
                        ) : null}
                      </DataListCell>,
                      <DataListCell key="counts" alignRight>
                        {buildSummary && buildSummary.counts.length > 0 ? (
                          buildSummary.counts.map(({ severity, count }) => (
                            <Label key={severity} color={SEVERITY_COLOUR[severity] ?? 'grey'} isCompact variant="outline">
                              {count} {severity}
                            </Label>
                          ))
                        ) : build.inventory === 'unparseable' || !buildSummary ? null : (
                          <Content component="small">{buildSummary.scanned} scanned</Content>
                        )}
                      </DataListCell>,
                      <DataListCell key="open" isIcon alignRight>
                        <AngleRightIcon aria-hidden />
                      </DataListCell>,
                    ]} />
                  </DataListItemRow>
                </DataListItem>
              )
            })}
          </DataList>
        </div>
      </CardBody>
    </Card>
  )
}
