import { useState, type ReactNode } from 'react'
import {
  Button, Content, Form, FormGroup, ModalBody, ModalFooter, TextInput, WizardFooterWrapper,
} from '@patternfly/react-core'

export type TenancyKind = 'organization' | 'project'

const tenancyCopy = {
  organization: {
    label: 'Organization name',
    helper: 'Contains projects and their principals. The name cannot be changed later.',
  },
  project: {
    label: 'Project name',
    helper: 'Scopes buckets, principals and channels. The name cannot be changed later.',
  },
} as const

/** The one name form used by first-run and steady-state tenancy creation. */
export function TenancyForm({
  kind, formID, fieldID = `${formID}-name`, submitLabel, submitting, footer, message,
  onSubmit, onCancel,
}: {
  kind: TenancyKind
  formID: string
  fieldID?: string
  submitLabel: string
  submitting: boolean
  footer: 'wizard' | 'modal'
  message?: ReactNode
  onSubmit: (name: string) => void | Promise<void>
  onCancel?: () => void
}) {
  const [name, setName] = useState('')
  const copy = tenancyCopy[kind]
  const submit = (
    <Button
      type="submit"
      form={formID}
      variant="primary"
      isLoading={submitting}
      isDisabled={submitting || name.trim() === ''}
    >
      {submitLabel}
    </Button>
  )

  const form = (
    <Form
      id={formID}
      style={{ marginTop: 16 }}
      onSubmit={(event) => {
        event.preventDefault()
        void onSubmit(name.trim())
      }}
    >
      <FormGroup label={copy.label} isRequired fieldId={fieldID}>
        <TextInput
          id={fieldID}
          value={name}
          onChange={(_event, value) => setName(value)}
          autoFocus
        />
        <Content component="small">{copy.helper}</Content>
      </FormGroup>
    </Form>
  )

  return footer === 'wizard' ? (
    <>
      {message}
      {form}
      <WizardFooterWrapper>{submit}</WizardFooterWrapper>
    </>
  ) : (
    <>
      <ModalBody>
        {message}
        {form}
      </ModalBody>
      <ModalFooter>
        {submit}
        <Button variant="link" onClick={onCancel} isDisabled={submitting}>Cancel</Button>
      </ModalFooter>
    </>
  )
}
