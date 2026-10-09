import { useState, type ReactNode } from 'react'
import {
  Button, Content, Form, FormGroup, ModalBody, ModalFooter, TextInput, WizardFooterWrapper,
} from '@patternfly/react-core'

export type TenancyKind = 'organization' | 'project'

const tenancyCopy = {
  organization: {
    label: 'Organization name',
    helper: 'Use a lowercase RFC 1123 DNS label: 1 to 63 characters using lowercase letters, digits and hyphens, with no leading or trailing hyphen. The name cannot be changed later.',
  },
  project: {
    label: 'Project name',
    helper: 'Scopes buckets, principals and channels. The name cannot be changed later.',
  },
} as const

const organizationNamePattern = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

export function organizationNameError(name: string): string | null {
  return organizationNamePattern.test(name)
    ? null
    : 'Organization name must be a lowercase RFC 1123 DNS label.'
}

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
  const trimmedName = name.trim()
  const nameFailure = kind === 'organization' ? organizationNameError(trimmedName) : null
  const nameInvalid = name !== '' && nameFailure !== null
  const nameValid = kind === 'organization' ? nameFailure === null : trimmedName !== ''
  const submit = (
    <Button
      type="submit"
      form={formID}
      variant="primary"
      isLoading={submitting}
      isDisabled={submitting || !nameValid}
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
        if (nameValid) void onSubmit(trimmedName)
      }}
    >
      <FormGroup label={copy.label} isRequired fieldId={fieldID}>
        <TextInput
          id={fieldID}
          value={name}
          onChange={(_event, value) => setName(value)}
          validated={nameInvalid ? 'error' : 'default'}
          aria-invalid={nameInvalid ? 'true' : undefined}
          aria-describedby={`${fieldID}-helper`}
          autoFocus
        />
        <Content component="small" id={`${fieldID}-helper`}>{copy.helper}</Content>
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
