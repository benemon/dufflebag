import { useRef, useState, type ReactNode } from 'react'
import {
  Button, Card, CardBody, CardTitle, ExpandableSection, Label, Skeleton, Title, Truncate,
} from '@patternfly/react-core'

import type {
  AncestryChild, AncestryFreshness, AncestryParent, Version,
} from '../data/versions'
import { When } from './When'
import './AncestryCard.css'

type Relation = AncestryParent | AncestryChild
type Kind = 'parents' | 'children'
/** One related version, carrying every relation that names it. */
type Row = { entry: Relation; relations: Relation[] }
type Group = { bucket: string; rows: Row[] }
/** The bucket page names, per row, which of its own versions the relation belongs to. */
export type AncestryScope =
  | { versionName: string }
  | { versions: Version[]; newest?: string }

const subtle = 'var(--pf-t--global--text--color--subtle)'

export function AncestryCard({
  bucket,
  scope,
  parents,
  children,
  loading = false,
  onOpenBucket,
  onOpenVersion,
}: {
  bucket: string
  scope: AncestryScope
  parents: Relation[]
  children: Relation[]
  loading?: boolean
  onOpenBucket: (bucket: string) => void
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  const subtitle = 'versionName' in scope ? `${bucket} ${scope.versionName}` : `${bucket} · all versions`
  return (
    <Card>
      <CardTitle>
        Ancestry
        <div className="ancestry-subtitle"><Truncate content={subtitle} /></div>
      </CardTitle>
      <CardBody className="ancestry-card-body">
        <div className="ancestry-sections" aria-busy={loading || undefined}>
          {(['parents', 'children'] as const).map((kind) => (
            <Section
              key={kind}
              kind={kind}
              entries={kind === 'parents' ? parents : children}
              scope={scope}
              loading={loading}
              onOpenBucket={onOpenBucket}
              onOpenVersion={onOpenVersion}
            />
          ))}
        </div>
      </CardBody>
    </Card>
  )
}

function Section({
  kind, entries, scope, loading, onOpenBucket, onOpenVersion,
}: {
  kind: Kind
  entries: Relation[]
  scope: AncestryScope
  loading: boolean
  onOpenBucket: (bucket: string) => void
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  const name = kind === 'parents' ? 'Parents' : 'Children'
  const groups = groupRelations(entries)
  const count = groups.reduce((total, group) => total + group.rows.length, 0)
  return (
    <section className="ancestry-section">
      <Title headingLevel="h3" size="md">{loading ? name : `${name} · ${count}`}</Title>
      {loading ? (
        [45, 70, 60].map((width, index) => (
          <Skeleton
            key={width}
            width={`${width}%`}
            screenreaderText={index === 0 ? `Loading ${name.toLowerCase()}…` : undefined}
          />
        ))
      ) : groups.length === 0 ? (
        <span style={{ color: subtle }}>{kind === 'parents' ? 'No parents' : 'No children'}</span>
      ) : groups.map((group) => (
        <GroupCard
          key={group.bucket}
          group={group}
          kind={kind}
          scope={scope}
          onOpenBucket={onOpenBucket}
          onOpenVersion={onOpenVersion}
        />
      ))}
    </section>
  )
}

function GroupCard({
  group, kind, scope, onOpenBucket, onOpenVersion,
}: {
  group: Group
  kind: Kind
  scope: AncestryScope
  onOpenBucket: (bucket: string) => void
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  const bucketLink = useRef<HTMLButtonElement>(null)
  const [expanded, setExpanded] = useState(false)
  const visible = group.rows.filter((row) => !isOlder(row, kind, scope))
  const older = group.rows.filter((row) => isOlder(row, kind, scope))
  // A group never shows only its caret row: its newest-sequence entry stays out.
  if (visible.length === 0) visible.push(older.shift()!)
  const bucketScope = 'versions' in scope
  const rowProps = { kind, scope, onOpenVersion }
  return (
    <div className="ancestry-group" role="group" aria-label={group.bucket}>
      <div className="ancestry-group-header">
        <span className="ancestry-group-name">
          <Button ref={bucketLink} variant="link" isInline onClick={() => onOpenBucket(group.bucket)}>
            <Truncate content={group.bucket} tooltipProps={{ triggerRef: bucketLink }} />
          </Button>
        </span>
        {kind === 'children' ? (
          <span className="ancestry-group-count">
            {group.rows.length} built from {bucketScope ? 'this bucket' : 'this'}
          </span>
        ) : null}
      </div>
      {visible.map((row) => <RowView key={row.entry.fingerprint} row={row} {...rowProps} />)}
      {older.length > 0 ? (
        <ExpandableSection
          className="ancestry-older"
          toggleText={olderText(older.length, kind, bucketScope)}
          isExpanded={expanded}
          onToggle={(_event, open) => setExpanded(open)}
        >
          {older.map((row) => <RowView key={row.entry.fingerprint} row={row} {...rowProps} />)}
        </ExpandableSection>
      ) : null}
    </div>
  )
}

function RowView({
  row, kind, scope, onOpenVersion,
}: {
  row: Row
  kind: Kind
  scope: AncestryScope
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  const { entry } = row
  const local = 'versions' in scope ? localContext(row, scope.versions) : ''
  const inherited = entry.revocationInheritedFrom
  return (
    <div className="ancestry-row">
      <div className="ancestry-row-labels">
        <Button
          variant="link"
          isInline
          className="ancestry-version"
          style={entry.state === 'revoked' ? { color: subtle } : undefined}
          onClick={() => onOpenVersion(entry.bucket, entry.fingerprint)}
        >
          {entry.versionName}
        </Button>
        {(entry.channels ?? []).map((channel) => <Label key={channel} isCompact>{channel}</Label>)}
        {entry.newest ? <Label isCompact variant="outline" className="ancestry-newest">Newest</Label> : null}
        {entry.state === 'revoked' ? <Label isCompact status="danger">Revoked</Label> : null}
        {entry.state === 'revocation-scheduled' ? (
          <Label isCompact status="warning" variant="outline">
            Revocation scheduled <When iso={entry.revokeAt} dateOnly />
          </Label>
        ) : null}
      </div>
      {local ? <Line>{kind === 'parents' ? 'For' : 'From'} {local}</Line> : null}
      {distinctFreshness(row.relations).map((freshness) => (
        <FreshnessLine key={freshnessText(freshness)} freshness={freshness} />
      ))}
      {inherited ? (
        <Line>
          Inherited from{' '}
          <Button variant="link" isInline onClick={() => onOpenVersion(inherited.bucket, inherited.fingerprint)}>
            {inherited.bucket} {inherited.versionName}
          </Button>
        </Line>
      ) : null}
    </div>
  )
}

function Line({ children, icon = '', color = subtle }: { children: ReactNode; icon?: string; color?: string }) {
  return (
    <span className="ancestry-line" style={{ color }}>
      <span aria-hidden="true" className="ancestry-line-icon">{icon}</span>
      <span>{children}</span>
    </span>
  )
}

function FreshnessLine({ freshness }: { freshness: AncestryFreshness }) {
  const text = freshnessText(freshness)
  if (!freshness.channel) return <Line icon="?">{text}</Line>
  switch (freshness.status) {
    case 'UP_TO_DATE':
      return <Line icon="✓" color="var(--pf-t--global--text--color--status--success--default)">{text}</Line>
    case 'OUT_OF_DATE':
      return <Line icon="↑" color="var(--pf-t--global--text--color--status--warning--default)">{text}</Line>
    default:
      return <Line icon="?">{text}</Line>
  }
}

/**
 * An empty channel name is both "Packer recorded none" and "that channel was
 * since deleted"; the API cannot tell them apart, so neither does the copy.
 */
function freshnessText({ status, channel, builtVersion, currentVersion }: AncestryFreshness): string {
  if (!channel) return `Built from ${builtVersion} · no channel to compare against`
  if (status === 'UP_TO_DATE') return `Built from ${channel} · still current`
  if (status === 'OUT_OF_DATE' && currentVersion) return `Built from ${channel} · ${channel} now at ${currentVersion}`
  return `Built from ${channel} · ${channel} has no version`
}

function distinctFreshness(relations: Relation[]): AncestryFreshness[] {
  const byText = new Map(relations.map((relation) => [freshnessText(relation.buildFreshness), relation.buildFreshness]))
  return [...byText.values()]
}

function groupRelations(entries: Relation[]): Group[] {
  const groups = new Map<string, Map<string, Row>>()
  for (const entry of entries) {
    const rows = groups.get(entry.bucket) ?? new Map<string, Row>()
    groups.set(entry.bucket, rows)
    const row = rows.get(entry.fingerprint)
    if (row) row.relations.push(entry)
    else rows.set(entry.fingerprint, { entry, relations: [entry] })
  }
  return [...groups]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([bucket, rows]) => ({
      bucket,
      rows: [...rows.values()].sort((left, right) =>
        sequence(right.entry.versionName) - sequence(left.entry.versionName)),
    }))
}

/** Ancestry version names are "v" + the version's sequence (handler.go ancestryVersionName). */
function sequence(versionName: string): number {
  return Number(/^v(\d+)$/.exec(versionName)?.[1] ?? -1)
}

/**
 * Unread state (a related bucket the session cannot read) never collapses. On
 * the bucket page a parent folds only when every local version it serves would.
 */
function isOlder(row: Row, kind: Kind, scope: AncestryScope): boolean {
  const { entry } = row
  if (entry.channels === undefined || entry.newest) return false
  if (kind === 'children' || !('versions' in scope)) return entry.channels.length === 0
  return row.relations.every((relation) => {
    const local = scope.versions.find((version) => version.name === relation.localVersionName)
    return !!local && local.channels.length === 0 && local.fingerprint !== scope.newest
  })
}

function olderText(count: number, kind: Kind, bucketScope: boolean): string {
  if (bucketScope && kind === 'parents') {
    return `${count} ${count === 1 ? 'parent' : 'parents'} of older versions`
  }
  return `${count} older ${count === 1 ? 'version' : 'versions'}`
}

/** "v4 · latest, production" per local version; "; " keeps each one's channels apart. */
function localContext(row: Row, versions: Version[]): string {
  const names = new Set(row.relations.map((relation) => relation.localVersionName))
  const contexts = versions.filter((version) => names.has(version.name)).map((version) => {
    const details = [version.channels.join(', '), version.state === 'revoked' ? 'revoked' : '']
      .filter(Boolean)
    return [version.name, ...details].join(' · ')
  })
  return contexts.join(contexts.some((context) => context.includes(' · ')) ? '; ' : ', ')
}
