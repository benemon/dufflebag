import type { ReactNode } from 'react'
import { Badge, Tab, Tabs, TabTitleText } from '@patternfly/react-core'

import './RegistryFacets.css'

export type FacetCount =
  | { status: 'known'; value: number }
  | { status: 'unknown' }

export type RegistryFacet<Key extends string> = {
  key: Key
  label: string
  count?: FacetCount
  content: ReactNode
}

export function FacetRail<Key extends string>({
  active,
  heading,
  label,
  facets,
  onSelect,
  unmountOnExit,
}: {
  active: Key
  heading: string
  label: string
  facets: RegistryFacet<Key>[]
  onSelect: (key: Key) => void
  unmountOnExit?: boolean
}) {
  return (
    <div className="registry-facet-layout">
      <nav className="registry-facet-rail" aria-label={label}>
        <div className="registry-facet-heading">{heading}</div>
        <Tabs
          activeKey={active}
          onSelect={(_event, eventKey) => onSelect(eventKey as Key)}
          isVertical
          tabListAriaLabel={label}
          unmountOnExit={unmountOnExit}
          className="registry-facet-tabs"
        >
          {facets.map((facet) => (
            <Tab
              key={facet.key}
              eventKey={facet.key}
              title={(
                <>
                  <TabTitleText>{facet.label}</TabTitleText>
                  {facet.count?.status === 'known' ? (
                    <Badge isRead>{facet.count.value}</Badge>
                  ) : null}
                </>
              )}
            >
              {facet.content}
            </Tab>
          ))}
        </Tabs>
      </nav>
    </div>
  )
}

export function knownCount(value: number): FacetCount {
  return { status: 'known', value }
}
