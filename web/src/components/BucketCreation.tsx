import { useState } from 'react'
import {
  Alert, Button, Content, Form, FormGroup, Modal, ModalBody, ModalFooter, ModalHeader,
  TextInput, type ButtonProps,
} from '@patternfly/react-core'

import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'

/**
 * The trigger alone — deliberately not the modal's owner. Its picker-footer
 * placement sits inside the Select's popper, which unmounts on any outside
 * click; a modal owned here vanished mid-submit and its failure state with it
 * (duf-3p03). The modal's state lives with the caller, outside the picker.
 */
export function CreateBucketButton({
  callerRole, refusal = null, onOpen, variant = 'primary',
}: {
  callerRole: Role | null
  refusal?: string | null
  onOpen: () => void
  variant?: ButtonProps['variant']
}) {
  return (
    <RoleRestrictedButton
      action="createBuckets"
      callerRole={callerRole}
      refusal={refusal}
      variant={variant}
      onClick={(event) => {
        event.stopPropagation()
        onOpen()
      }}
    >
      Create bucket
    </RoleRestrictedButton>
  )
}

export function BucketModal({
  onCreate, onClose,
}: {
  onCreate: (name: string) => Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const submit = async (name: string) => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onCreate(name)
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The bucket could not be created.')
    } finally {
      setSubmitting(false)
    }
  }
  return (
    <BucketModalView
      submitting={submitting}
      failure={failure}
      onSubmit={submit}
      onClose={onClose}
    />
  )
}

export function BucketModalView({
  submitting, failure, onSubmit, onClose,
}: {
  submitting: boolean
  failure: string | null
  onSubmit: (name: string) => Promise<void>
  onClose: () => void
}) {
  const [name, setName] = useState('')
  return (
    <Modal aria-labelledby="create-bucket-modal-title" isOpen onClose={onClose} variant="small">
      <ModalHeader labelId="create-bucket-modal-title" title="Create bucket" />
      <ModalBody>
        {failure ? (
          <Alert variant="danger" isInline title="The bucket could not be created">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Form
          id="create-bucket"
          style={{ marginTop: 16 }}
          onSubmit={(event) => {
            event.preventDefault()
            void onSubmit(name.trim())
          }}
        >
          <FormGroup label="Bucket name" isRequired fieldId="create-bucket-name">
            <TextInput
              id="create-bucket-name"
              value={name}
              onChange={(_event, value) => setName(value)}
              autoFocus
            />
            <Content component="small">
              Stores versions and channels. The name cannot be changed later.
            </Content>
          </FormGroup>
        </Form>
      </ModalBody>
      <ModalFooter>
        <Button
          type="submit"
          form="create-bucket"
          variant="primary"
          isLoading={submitting}
          isDisabled={submitting || name.trim() === ''}
        >
          Create bucket
        </Button>
        <Button variant="link" onClick={onClose} isDisabled={submitting}>Cancel</Button>
      </ModalFooter>
    </Modal>
  )
}
