import { useState } from 'react'
import {
  Alert, Button, Card, CardBody, CardTitle, Content, PageSection, Title,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'
import { When } from '../components/When'
import {
  encryptionRefusalHint, rewrapEncryption, rotateEncryption, useEncryption,
  type Encryption as EncryptionData, type KeyringEntry,
} from '../data/encryption'

export function Encryption() {
  return <EncryptionView {...useEncryption()} />
}

type ViewProps = ReturnType<typeof useEncryption>

export function EncryptionView({
  encryption, loading, failure, reload, token, callerRole,
}: ViewProps) {
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [confirmingRotation, setConfirmingRotation] = useState(false)

  async function run(work: () => Promise<void>) {
    setActionFailure(null)
    try {
      await work()
      await reload()
    } catch (err: unknown) {
      setActionFailure(
        encryptionRefusalHint(err) ?? (err instanceof Error ? err.message : 'The action failed.'),
      )
    }
  }

  return (
    <>
      <PageSection variant="default">
        <Title headingLevel="h1" size="2xl">Encryption</Title>
        <Content component="p">
          Encryption at rest protects stored data with a keyring backed by an external key
          service. This state is visible only to root; other roles can open this page but the
          server refuses access.
        </Content>
      </PageSection>

      <PageSection variant="secondary" isFilled>
        <EncryptionAlerts failure={failure} actionFailure={actionFailure} />

        {loading ? (
          <Content component="p">Loading encryption state…</Content>
        ) : failure ? null : encryptionIsUnconfigured(encryption) ? (
          <Alert variant="info" isInline title="Encryption at rest is not configured on this instance.">
            <Content component="p">
              Encryption is chosen at first boot and cannot be enabled later on this instance.
            </Content>
          </Alert>
        ) : encryption ? (
          <KeyringCard
            encryption={encryption}
            callerRole={callerRole}
            confirmingRotation={confirmingRotation}
            onRewrap={() => {
              void run(async () => {
                if (token) await rewrapEncryption(token)
              })
            }}
            onRotate={() => setConfirmingRotation(true)}
            onCancelRotation={() => setConfirmingRotation(false)}
            onConfirmRotation={() => {
              setConfirmingRotation(false)
              void run(async () => {
                if (token) await rotateEncryption(token)
              })
            }}
          />
        ) : null}
      </PageSection>
    </>
  )
}

export function encryptionIsUnconfigured(encryption: EncryptionData | null): boolean {
  return encryption?.state === 'unconfigured'
}

export function EncryptionAlerts({
  failure, actionFailure,
}: {
  failure: string | null
  actionFailure: string | null
}) {
  return (
    <>
      {failure ? (
        <Alert variant="danger" isInline title="Encryption state could not be loaded">
          <Content component="p">{failure}</Content>
        </Alert>
      ) : null}
      {actionFailure ? (
        <Alert variant="danger" isInline title="The action was refused">
          <Content component="p">{actionFailure}</Content>
        </Alert>
      ) : null}
    </>
  )
}

export function KeyringCard({
  encryption, callerRole, confirmingRotation, onRewrap, onRotate,
  onCancelRotation, onConfirmRotation,
}: {
  encryption: EncryptionData
  callerRole: Role | null
  confirmingRotation: boolean
  onRewrap: () => void
  onRotate: () => void
  onCancelRotation: () => void
  onConfirmRotation: () => void
}) {
  return (
    <Card>
      <CardTitle>Keyring</CardTitle>
      <CardBody>
        {encryption.state === 'degraded' ? <DegradedEncryptionWarning /> : null}
        {confirmingRotation ? (
          <RotationConfirmation
            callerRole={callerRole}
            onConfirm={onConfirmRotation}
            onCancel={onCancelRotation}
          />
        ) : null}
        <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
          <RoleRestrictedButton
            action="manageEncryption" callerRole={callerRole} variant="secondary"
            onClick={onRewrap}
          >
            Rewrap keyring
          </RoleRestrictedButton>
          <RoleRestrictedButton
            action="manageEncryption" callerRole={callerRole} variant="primary"
            onClick={onRotate}
          >
            Rotate keys
          </RoleRestrictedButton>
        </div>
        <KeyringTable entries={encryption.keyring} />
      </CardBody>
    </Card>
  )
}

export function DegradedEncryptionWarning() {
  return (
    <Alert
      variant="warning"
      isInline
      title="The key service could not unwrap the keyring at its last heartbeat"
      style={{ marginBottom: 16 }}
    >
      <Content component="p">
        Rewrap and rotate will fail until the key service recovers. A replica restarted now
        would refuse to serve ("sealed").
      </Content>
    </Alert>
  )
}

export function RotationConfirmation({
  callerRole, onConfirm, onCancel,
}: {
  callerRole: Role | null
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Alert
      variant="warning"
      isInline
      title="Rotate every encryption key?"
      style={{ marginBottom: 16 }}
    >
      <Content component="p">
        Existing data stays readable forever. Tokens signed with the old key age out within 15
        minutes. Audit HMAC correlation across the rotation boundary changes by design. On
        multi-replica deployments, peers adopt the new keys within about five minutes.
      </Content>
      <RoleRestrictedButton
        action="manageEncryption" callerRole={callerRole} variant="danger" onClick={onConfirm}
      >
        Rotate keys
      </RoleRestrictedButton>
      <Button variant="link" onClick={onCancel}>Cancel</Button>
    </Alert>
  )
}

export type KeyringPurposeSummary = {
  purpose: string
  activeVersion: number
  retained: number
  kekRefs: string
  wrappedAt: string
}

/**
 * One row per purpose, values updating in place: rotation would otherwise add
 * four rows forever, and the row count says nothing the retained count does
 * not. A purpose whose entries name more than one KEK version renders them
 * all — that mixed state is exactly what a pending rewrap looks like.
 */
export function summarizeKeyring(entries: KeyringEntry[]): KeyringPurposeSummary[] {
  const byPurpose = new Map<string, KeyringEntry[]>()
  for (const entry of entries) {
    const rows = byPurpose.get(entry.purpose) ?? []
    rows.push(entry)
    byPurpose.set(entry.purpose, rows)
  }
  return [...byPurpose.entries()].map(([purpose, rows]) => ({
    purpose,
    activeVersion: Math.max(...rows.map((row) => row.version)),
    retained: rows.length,
    kekRefs: [...new Set(rows.map((row) => row.kek_ref))].sort().join(', '),
    wrappedAt: rows.map((row) => row.wrapped_at).sort().at(-1) ?? '',
  }))
}

export function KeyringTable({ entries }: { entries: KeyringEntry[] }) {
  return (
    <>
      <Content component="p">
        Every key version ever minted is retained, so rotation grows the
        retained count — existing data stays readable forever. KEK version is
        the key service&apos;s key version the stored entries are wrapped
        under: it advances only after the KEK is rotated at the key service
        and the keyring is rewrapped here. More than one KEK version listed
        against a purpose means a rewrap is still owed.
      </Content>
      <Table aria-label="Encryption keyring" variant="compact">
        <Thead>
          <Tr>
            <Th>Purpose</Th>
            <Th>Active</Th>
            <Th>Retained</Th>
            <Th>KEK version</Th>
            <Th>Wrapped at</Th>
          </Tr>
        </Thead>
        <Tbody>
          {summarizeKeyring(entries).map((summary) => (
            <Tr key={summary.purpose}>
              <Td dataLabel="Purpose">{summary.purpose}</Td>
              <Td dataLabel="Active">{summary.activeVersion}</Td>
              <Td dataLabel="Retained">{summary.retained}</Td>
              <Td dataLabel="KEK version">{summary.kekRefs}</Td>
              <Td dataLabel="Wrapped at"><When iso={summary.wrappedAt} /></Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
    </>
  )
}
