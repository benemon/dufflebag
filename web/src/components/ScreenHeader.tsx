import type { ReactNode } from 'react'
import { Button, Content, PageSection, Title } from '@patternfly/react-core'
import SyncAltIcon from '@patternfly/react-icons/dist/esm/icons/sync-alt-icon'

export function ScreenHeader({
  title, description, actions, breadcrumbs, children, onRefresh, refreshing,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  breadcrumbs?: ReactNode
  children?: ReactNode
  onRefresh?: () => void
  refreshing?: boolean
}) {
  return (
    <PageSection variant="default">
      {breadcrumbs}
      {title || description || actions || onRefresh ? (
        <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            {title ? <Title headingLevel="h1" size="2xl">{title}</Title> : null}
            {description ? <Content component="p">{description}</Content> : null}
          </div>
          {onRefresh || actions ? (
            <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
              {onRefresh ? (
                <Button
                  variant="plain"
                  aria-label="Refresh"
                  isDisabled={refreshing}
                  icon={<SyncAltIcon />}
                  onClick={onRefresh}
                />
              ) : null}
              {actions}
            </div>
          ) : null}
        </div>
      ) : null}
      {children}
    </PageSection>
  )
}
