import { useState } from 'react'
import {
  Alert, Button, Content, Modal, ModalBody, ModalFooter, ModalHeader,
} from '@patternfly/react-core'

import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'

export function DeleteBucketModal({
  bucket, callerRole, onConfirm, onClose,
}: {
  bucket: string
  callerRole: Role | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
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
        onConfirm={confirm}
        onClose={onClose}
      />
    </Modal>
  )
}

export function DeleteBucketModalView({
  bucket, callerRole, submitting, failure, onConfirm, onClose,
}: {
  bucket: string
  callerRole: Role | null
  submitting: boolean
  failure: string | null
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
