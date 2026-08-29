import { Button, Tooltip, type ButtonProps } from '@patternfly/react-core'

import {
  permitsAction, requirementReason, type ConsoleAction, type Role,
} from './permissions'

export function RoleRestrictedButton({
  action, callerRole, refusal = null, children, isDisabled, ...props
}: ButtonProps & {
  action: ConsoleAction
  callerRole: Role | null
  /**
   * A refusal that no role change can lift, such as a scope or current-state
   * denial. When set it wins over the role reason: telling a bucket-scoped
   * session "Requires builder" would send it chasing a role it may already
   * hold.
   */
  refusal?: string | null
}) {
  const refused = refusal != null || !permitsAction(callerRole, action)
  const reason = refusal ?? requirementReason(action)
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
