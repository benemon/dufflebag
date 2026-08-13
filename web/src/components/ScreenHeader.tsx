import type { ReactNode } from 'react'
import { Content, PageSection, Title } from '@patternfly/react-core'

export function ScreenHeader({
  title, description, actions, breadcrumbs, children,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  breadcrumbs?: ReactNode
  children?: ReactNode
}) {
  return (
    <PageSection variant="default">
      {breadcrumbs}
      {title || description || actions ? (
        <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            {title ? <Title headingLevel="h1" size="2xl">{title}</Title> : null}
            {description ? <Content component="p">{description}</Content> : null}
          </div>
          {actions ? (
            <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>{actions}</div>
          ) : null}
        </div>
      ) : null}
      {children}
    </PageSection>
  )
}
