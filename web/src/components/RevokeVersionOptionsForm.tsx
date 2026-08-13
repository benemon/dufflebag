import { Checkbox, Content, Form, FormGroup, Radio, TextArea, TextInput } from '@patternfly/react-core'

import type { RevokeVersionOptions } from '../api/client'

export type RevokeWhen = 'now' | 'scheduled'

export function revokeScheduleFailure(when: RevokeWhen, scheduledAt: string): string | null {
  if (when === 'now') return null
  const parsed = scheduledAt === '' ? null : new Date(scheduledAt)
  if (parsed === null || Number.isNaN(parsed.getTime())) return 'Choose a scheduled time.'
  return parsed.getTime() <= Date.now() ? 'Scheduled time must be in the future.' : null
}

export function revokeOptions({
  message, when, scheduledAt, skipDescendants, disableRollback,
}: {
  message: string
  when: RevokeWhen
  scheduledAt: string
  skipDescendants: boolean
  disableRollback: boolean
}): RevokeVersionOptions {
  return {
    revoke_at: when === 'now' ? new Date().toISOString() : new Date(scheduledAt).toISOString(),
    ...(message.trim() ? { revocation_message: message.trim() } : {}),
    ...(skipDescendants ? { skip_descendants_revocation: true } : {}),
    ...(disableRollback ? { disable_rollback_channels: true } : {}),
  }
}

export function RevokeVersionOptionsForm({
  idPrefix, message, when, scheduledAt, skipDescendants, disableRollback,
  scheduleFailure, onMessageChange, onWhenChange, onScheduledAtChange,
  onSkipDescendantsChange, onDisableRollbackChange,
}: {
  idPrefix: string
  message: string
  when: RevokeWhen
  scheduledAt: string
  skipDescendants: boolean
  disableRollback: boolean
  scheduleFailure: string | null
  onMessageChange: (message: string) => void
  onWhenChange: (when: RevokeWhen) => void
  onScheduledAtChange: (scheduledAt: string) => void
  onSkipDescendantsChange: (checked: boolean) => void
  onDisableRollbackChange: (checked: boolean) => void
}) {
  return (
    <Form>
      <FormGroup label="Revocation message" fieldId={`${idPrefix}-message`}>
        <TextArea
          id={`${idPrefix}-message`}
          value={message}
          resizeOrientation="vertical"
          onChange={(_event, value) => onMessageChange(value)}
        />
      </FormGroup>
      <FormGroup label="When" fieldId={`${idPrefix}-when`} role="radiogroup">
        <Radio
          id={`${idPrefix}-now`}
          name={`${idPrefix}-when`}
          label="Now"
          isChecked={when === 'now'}
          onChange={() => onWhenChange('now')}
        />
        <Radio
          id={`${idPrefix}-scheduled`}
          name={`${idPrefix}-when`}
          label="At a scheduled time"
          isChecked={when === 'scheduled'}
          onChange={() => onWhenChange('scheduled')}
        />
      </FormGroup>
      {when === 'scheduled' ? (
        <FormGroup label="Scheduled time" isRequired fieldId={`${idPrefix}-scheduled-at`}>
          <TextInput
            id={`${idPrefix}-scheduled-at`}
            type="datetime-local"
            value={scheduledAt}
            validated={scheduleFailure ? 'error' : 'default'}
            aria-invalid={scheduleFailure ? 'true' : undefined}
            aria-describedby={scheduleFailure ? `${idPrefix}-scheduled-at-error` : undefined}
            onChange={(_event, value) => onScheduledAtChange(value)}
          />
          {scheduleFailure ? (
            <Content component="p" id={`${idPrefix}-scheduled-at-error`}>
              {scheduleFailure}
            </Content>
          ) : null}
        </FormGroup>
      ) : null}
      <Checkbox
        id={`${idPrefix}-skip-descendants`}
        label="Skip descendant revocation"
        description="Leave descendant versions active."
        isChecked={skipDescendants}
        onChange={(_event, checked) => onSkipDescendantsChange(checked)}
      />
      <Checkbox
        id={`${idPrefix}-disable-rollback`}
        label="Do not roll channels back"
        description="Leave channel assignments unchanged."
        isChecked={disableRollback}
        onChange={(_event, checked) => onDisableRollbackChange(checked)}
      />
    </Form>
  )
}
