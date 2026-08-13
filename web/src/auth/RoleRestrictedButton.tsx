import { Button, Tooltip, type ButtonProps } from '@patternfly/react-core'

import {
  permitsAction, requirementReason, type ConsoleAction, type Role,
} from './permissions'

export function RoleRestrictedButton({
  action, callerRole, children, isDisabled, ...props
}: ButtonProps & {
  action: ConsoleAction
  callerRole: Role | null
}) {
  const refused = !permitsAction(callerRole, action)
  const reason = requirementReason(action)
  const button = (
    <Button {...props} isDisabled={isDisabled || refused}>
      {children}
      {refused ? <span className="pf-v6-u-screen-reader"> — {reason}</span> : null}
    </Button>
  )
  if (!refused) return button
  return (
    // A natively disabled button dispatches no events, and PF's aria-disabled
    // variant swallows injected handlers — so a click-triggered Popover on the
    // control itself can never open. The focusable wrapper with a hover/focus
    // Tooltip is PatternFly's canonical reachable pattern; keep it.
    <Tooltip content={reason}>
      <span tabIndex={0} aria-label={reason} style={{ display: 'inline-block' }}>
        {button}
      </span>
    </Tooltip>
  )
}
