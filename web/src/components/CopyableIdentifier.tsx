import { ClipboardCopy, ClipboardCopyVariant } from '@patternfly/react-core'

/** A compact, single-rendering identifier that readers copy instead of reading through. */
export function CopyableIdentifier({ value, label }: { value: string; label: string }) {
  return (
    <ClipboardCopy
      variant={ClipboardCopyVariant.inlineCompact}
      isReadOnly
      isCode
      truncation
      textAriaLabel={label}
      hoverTip="Copy"
      clickTip="Copied"
      style={{ fontFamily: 'Red Hat Mono, monospace', maxWidth: '100%' }}
    >
      {value}
    </ClipboardCopy>
  )
}
