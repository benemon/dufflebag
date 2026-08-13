import { Label, Truncate } from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import AngleDownIcon from '@patternfly/react-icons/dist/esm/icons/angle-down-icon'
import AngleRightIcon from '@patternfly/react-icons/dist/esm/icons/angle-right-icon'

import { severityCounts, type Severity } from '../data/findings'
import type { Finding } from '../data/versions'

/**
 * The word "clean" never appears here, and neither does "stale". A registry
 * that has not examined something must not imply it is safe, and a figure that
 * has stopped being maintained is shown as an as-of fact rather than labelled
 * with a word users skim past.
 */

const SEVERITY_COLOUR: Record<string, 'red' | 'orange' | 'yellow' | 'blue' | 'grey'> = {
  critical: 'red',
  high: 'orange',
  medium: 'yellow',
  low: 'blue',
  negligible: 'grey',
  unknown: 'grey',
}

/** The class that marks figures no longer being maintained. */
export const OUT_OF_SCAN_SET_CLASS = 'dfbg-findings-unmaintained'

/**
 * The findings cell: one chip per severity band with its count, worst first.
 * Counts rather than prose so the column has a fixed width — the previous
 * rendering put four facts per finding inline and wrapped off the screen.
 * A package with nothing found shows a dash and offers nothing to open.
 */
export function FindingCounts({
  findings, isExpanded,
}: {
  findings?: Finding[]
  /** Present only where the cell can be expanded, so the caret is the signal
   *  that there is something to open — the chips alone read as static. */
  isExpanded?: boolean
}) {
  const counts = severityCounts(findings ?? [])
  if (counts.length === 0) {
    return <span style={{ color: 'var(--pf-t--global--text--color--disabled)' }} data-findings="none">—</span>
  }
  return (
    <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }} data-findings="counts">
      {isExpanded !== undefined && (
        <span data-caret={isExpanded ? 'open' : 'closed'} style={{ color: 'var(--pf-t--global--text--color--subtle)', display: 'inline-flex' }}>
          {isExpanded ? <AngleDownIcon /> : <AngleRightIcon />}
        </span>
      )}
      {counts.map(({ severity, count }) => (
        <Label key={severity} color={SEVERITY_COLOUR[severity] ?? 'grey'} isCompact>
          {count} {severity}
        </Label>
      ))}
    </span>
  )
}

/**
 * The expanded region beneath an affected package: a real table, so the CVSS
 * vector has a column of its own instead of running into the next sentence.
 * Rendered only for packages that have findings, so a clean row never gains
 * an expander.
 */
export function PackageFindingsTable({ findings }: { findings: Finding[] }) {
  const ordered = orderedFindings(findings)
  return (
    <Table aria-label="Findings" variant="compact" borders={false} data-findings-table="true">
      <Thead>
        <Tr>
          <Th width={20}>Advisory</Th>
          <Th width={10}>Severity</Th>
          <Th>Reported</Th>
          <Th width={20}>Aliases</Th>
          <Th width={15}>Fixed in</Th>
        </Tr>
      </Thead>
      <Tbody>
        {ordered.map((finding) => (
          <Tr key={finding.identifier}>
            <Td dataLabel="Advisory"><code>{finding.identifier}</code></Td>
            <Td dataLabel="Severity">
              <Label color={SEVERITY_COLOUR[finding.criticality as Severity] ?? 'grey'} isCompact>
                {finding.criticality}
              </Label>
            </Td>
            {/* The provider's verbatim value: severity scales are not
                comparable between providers, so it is shown rather than
                translated. Truncated with the full value on hover — it is a
                tell-me-if-I-ask fact, not a scanning one. */}
            <Td dataLabel="Reported" modifier="truncate">
              <code style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
                {finding.severity ? <Truncate content={finding.severity} /> : '—'}
              </code>
            </Td>
            <Td dataLabel="Aliases" modifier="truncate">
              <code style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
                {finding.aliases.length > 0 ? <Truncate content={finding.aliases.join(', ')} /> : '—'}
              </code>
            </Td>
            <Td dataLabel="Fixed in">{fixedVersions(finding)}</Td>
          </Tr>
        ))}
      </Tbody>
    </Table>
  )
}

/**
 * Multiple fixed versions are stored verbatim as a set rather than reduced to
 * a semantic minimum, so they are shown one per line instead of joined into a
 * string that wraps.
 */
function fixedVersions(finding: Finding) {
  const versions = finding.fixedVersion.split(',').map((v) => v.trim()).filter(Boolean)
  if (versions.length === 0) return <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>No fix available</span>
  return (
    <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      {versions.map((version) => (
        <code key={version} style={{ wordBreak: 'break-all' }}>{version}</code>
      ))}
    </span>
  )
}

/** Worst first, so the panel opens on what matters. */
function orderedFindings(findings: Finding[]): Finding[] {
  const rank = (f: Finding) =>
    SEVERITY_ORDER_INDEX[(f.criticality ?? 'unknown').toLowerCase()] ?? 0
  return [...findings].sort((a, b) => rank(b) - rank(a))
}

const SEVERITY_ORDER_INDEX: Record<string, number> = {
  unknown: 0, negligible: 1, low: 2, medium: 3, high: 4, critical: 5,
}
