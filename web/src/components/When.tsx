import { Timestamp } from '@patternfly/react-core'

export function When({
  iso, emptyText = '—', dateOnly = false,
}: {
  iso: string | null | undefined
  emptyText?: string
  dateOnly?: boolean
}) {
  if (!iso) return emptyText
  return (
    <Timestamp
      date={new Date(iso)}
      dateTime={iso}
      dateFormat="medium"
      timeFormat={dateOnly ? undefined : 'short'}
      tooltip={{ variant: 'default' }}
    />
  )
}
