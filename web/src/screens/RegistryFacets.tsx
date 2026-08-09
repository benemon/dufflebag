import type { ReactNode } from 'react'

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
        {facets.map((facet) => (
          <button
            key={facet.key}
            type="button"
            className="registry-facet"
            aria-current={facet.key === active}
            onClick={() => onSelect(facet.key)}
          >
            <span className="registry-facet-label">{facet.label}</span>
            <span className="registry-facet-count">{facetCountText(facet.count)}</span>
          </button>
        ))}
      </nav>
      {facets
        .filter((facet) => !unmountOnExit || facet.key === active)
        .map((facet) => (
          <div
            key={facet.key}
            className="registry-facet-content"
            hidden={facet.key !== active}
          >
            {facet.content}
          </div>
        ))}
    </div>
  )
}

export function knownCount(value: number): FacetCount {
  return { status: 'known', value }
}

export function facetCountText(count?: FacetCount): string {
  if (!count) return ''
  if (count.status === 'unknown') return ''
  return String(count.value)
}
