import { useState, type ReactNode } from 'react'
import {
  Button, Content, Form, FormGroup, Modal, ModalBody, ModalFooter, ModalHeader, TextInput,
} from '@patternfly/react-core'

type TypedConfirmModalProps = {
  title: string
  body: ReactNode
  expected: string
  verb: string
  onConfirm: () => void | Promise<void>
  onCancel: () => void
  busy: boolean
  /** A site may retain an existing validation gate in addition to the typed match. */
  confirmDisabled?: boolean
}

export function TypedConfirmModal(props: TypedConfirmModalProps) {
  const [confirmation, setConfirmation] = useState('')

  return (
    <Modal aria-labelledby="typed-confirm-modal-title" isOpen onClose={props.onCancel} variant="small">
      <TypedConfirmModalView
        {...props}
        confirmation={confirmation}
        onConfirmationChange={setConfirmation}
      />
    </Modal>
  )
}

export function TypedConfirmModalView({
  title, body, expected, verb, onConfirm, onCancel, busy, confirmDisabled = false,
  confirmation, onConfirmationChange,
}: TypedConfirmModalProps & {
  confirmation: string
  onConfirmationChange: (confirmation: string) => void
}) {
  const inputID = 'typed-confirm-modal-input'
  return (
    <>
      <ModalHeader labelId="typed-confirm-modal-title" title={title} />
      <ModalBody>
        {body}
        <Content component="p">Type <strong>{expected}</strong> to confirm.</Content>
        <Form>
          <FormGroup label="Confirmation" isRequired fieldId={inputID}>
            <TextInput
              id={inputID}
              aria-label={`${verb} confirmation`}
              autoFocus
              value={confirmation}
              onChange={(_event, value) => onConfirmationChange(value)}
            />
          </FormGroup>
        </Form>
      </ModalBody>
      <ModalFooter>
        <Button
          variant="danger"
          isLoading={busy}
          isDisabled={busy || confirmDisabled || confirmation !== expected}
          onClick={onConfirm}
        >
          {verb}
        </Button>
        <Button variant="link" isDisabled={busy} onClick={onCancel}>Cancel</Button>
      </ModalFooter>
    </>
  )
}
