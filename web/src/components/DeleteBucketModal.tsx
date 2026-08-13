import { useEffect, useState } from 'react'
import {
  Alert, Content,
} from '@patternfly/react-core'

import type { Role } from '../auth/permissions'
import { TypedConfirmModal } from './TypedConfirmModal'

export function DeleteBucketModal({
  bucket, callerRole, onConfirm, onClose, checkMirrored,
}: {
  bucket: string
  callerRole: Role | null
  onConfirm: () => Promise<void>
  onClose: () => void
  /**
   * Best-effort: whether Bag Drop actively mirrors this bucket. A failed or
   * absent check stays silent — the warning only asserts what is known, and
   * never blocks the delete.
   */
  checkMirrored?: () => Promise<boolean>
}) {
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [mirrored, setMirrored] = useState(false)
  // The modal mounts fresh on each open, so the check runs once per open —
  // deliberately not keyed on the callback's identity.
  useEffect(() => {
    if (!checkMirrored) return
    let cancelled = false
    checkMirrored()
      .then((value) => { if (!cancelled) setMirrored(value) })
      .catch(() => {})
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const confirm = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm()
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }
  return <DeleteBucketModalView
    bucket={bucket}
    callerRole={callerRole}
    submitting={submitting}
    failure={failure}
    mirrored={mirrored}
    onConfirm={confirm}
    onClose={onClose}
  />
}

export function DeleteBucketModalView({
  bucket, submitting, failure, mirrored = false, onConfirm, onClose,
}: {
  bucket: string
  callerRole: Role | null
  submitting: boolean
  failure: string | null
  mirrored?: boolean
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  return (
    <TypedConfirmModal
      title={`Delete ${bucket}`}
      expected={bucket}
      verb="Delete bucket"
      busy={submitting}
      onConfirm={onConfirm}
      onCancel={onClose}
      body={<>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Content component="p">
          Deleting {bucket} permanently removes the bucket and all its versions, builds,
          artifacts, channels and history.
        </Content>
        {mirrored ? (
          <Alert variant="warning" isInline title="This bucket is mirrored by Bag Drop">
            <Content component="p">
              Deleting it also deletes the destination copy at the next reconcile.
            </Content>
          </Alert>
        ) : null}
      </>}
    />
  )
}
