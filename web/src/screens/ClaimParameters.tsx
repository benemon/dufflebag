import {
  Alert, Content, Form, FormGroup, TextInput, Title,
} from '@patternfly/react-core'

import { type InitRequest } from '../api/client'

export function validateRecoveryParameters(shareCount: string, threshold: string): {
  request: InitRequest | null
  failure: { field: 'count' | 'threshold'; message: string } | null
} {
  const recoveryShareCount = Number(shareCount)
  const recoveryThreshold = Number(threshold)
  if (!Number.isInteger(recoveryShareCount) || recoveryShareCount < 1 || recoveryShareCount > 255) {
    return {
      request: null,
      failure: { field: 'count', message: 'Recovery share count must be a whole number from 1 to 255.' },
    }
  }
  if (!Number.isInteger(recoveryThreshold) || recoveryThreshold < 1) {
    return {
      request: null,
      failure: { field: 'threshold', message: 'Recovery threshold must be a whole number of at least 1.' },
    }
  }
  if (recoveryThreshold > recoveryShareCount) {
    return {
      request: null,
      failure: { field: 'threshold', message: 'Recovery threshold cannot exceed the share count.' },
    }
  }
  return {
    request: {
      recovery_share_count: recoveryShareCount,
      recovery_threshold: recoveryThreshold,
    },
    failure: null,
  }
}

export function ClaimParametersForm({
  shareCount,
  threshold,
  validation,
  onShareCountChange,
  onThresholdChange,
  onClaim,
}: {
  shareCount: string
  threshold: string
  validation: ReturnType<typeof validateRecoveryParameters>
  onShareCountChange: (value: string) => void
  onThresholdChange: (value: string) => void
  onClaim: (request: InitRequest) => void
}) {
  const countInvalid = validation.failure?.field === 'count'
  const thresholdInvalid = validation.failure?.field === 'threshold'
  return (
    <>
      <Alert variant="warning" isInline title="This instance is uninitialized">
        <Content component="p">
          Whoever completes this flow first owns the deployment. Do not expose an uninitialized
          instance publicly.
        </Content>
      </Alert>
      <Title headingLevel="h2" size="xl" style={{ marginTop: 16 }}>Before you continue</Title>
      <Content component="p">
        Initialization happens once and cannot be repeated or undone. It creates only the
        first root principal; you will name the first tenancy in the next two steps.
      </Content>
      <Form
        id="initialize-claim"
        style={{ marginTop: 16 }}
        onSubmit={(event) => {
          event.preventDefault()
          if (validation.request) onClaim(validation.request)
        }}
      >
        <FormGroup label="Recovery share count" isRequired fieldId="recovery-share-count">
          <TextInput
            id="recovery-share-count"
            type="number"
            min={1}
            max={255}
            step={1}
            value={shareCount}
            validated={countInvalid ? 'error' : 'default'}
            aria-invalid={countInvalid ? 'true' : undefined}
            aria-describedby={countInvalid ? 'recovery-parameters-error' : undefined}
            onChange={(_event, value) => onShareCountChange(value)}
          />
          <Content component="small">How many recovery shares are minted.</Content>
        </FormGroup>
        <FormGroup label="Recovery threshold" isRequired fieldId="recovery-threshold">
          <TextInput
            id="recovery-threshold"
            type="number"
            min={1}
            max={shareCount}
            step={1}
            value={threshold}
            validated={thresholdInvalid ? 'error' : 'default'}
            aria-invalid={thresholdInvalid ? 'true' : undefined}
            aria-describedby={thresholdInvalid ? 'recovery-parameters-error' : undefined}
            onChange={(_event, value) => onThresholdChange(value)}
          />
          <Content component="small">How many shares POST /sys/recovery demands.</Content>
        </FormGroup>
        {validation.failure ? (
          <Content component="p" id="recovery-parameters-error">
            {validation.failure.message}
          </Content>
        ) : null}
      </Form>
    </>
  )
}

