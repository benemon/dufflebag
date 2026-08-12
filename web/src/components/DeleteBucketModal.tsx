import { useEffect, useState } from 'react'
import {
  Alert, Button, Content, Modal, ModalBody, ModalFooter, ModalHeader,
} from '@patternfly/react-core'

import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'

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
  return (
    <Modal aria-labelledby="delete-bucket-modal-title" isOpen onClose={onClose} variant="small">
      <DeleteBucketModalView
        bucket={bucket}
        callerRole={callerRole}
        submitting={submitting}
        failure={failure}
        mirrored={mirrored}
        onConfirm={confirm}
        onClose={onClose}
      />
    </Modal>
  )
}

export function DeleteBucketModalView({
  bucket, callerRole, submitting, failure, mirrored = false, onConfirm, onClose,
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
    <>
      <ModalHeader labelId="delete-bucket-modal-title" title={`Delete ${bucket}`} />
      <ModalBody>
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
      </ModalBody>
      <ModalFooter>
        <RoleRestrictedButton
          action="deleteBuckets"
          callerRole={callerRole}
          variant="danger"
          isLoading={submitting}
          isDisabled={submitting}
          onClick={onConfirm}
        >
          Delete bucket
        </RoleRestrictedButton>
        <Button variant="link" isDisabled={submitting} onClick={onClose}>Cancel</Button>
      </ModalFooter>
    </>
  )
}
