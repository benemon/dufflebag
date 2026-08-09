import type { ComponentType } from 'react'
import { Tooltip } from '@patternfly/react-core'
import DockerIcon from '@patternfly/react-icons/dist/esm/icons/docker-icon'

// Keys are platform values observed in real build metadata — the demo
// registry's stock-Packer publishes — never guessed builder names. Packer's
// platform values are an open set, so the unmapped path below is the
// load-bearing one: an unknown platform renders its literal name.
const PLATFORM_ICONS: Record<string, ComponentType<{ title?: string }>> = {
  docker: DockerIcon,
}

// The icon's title carries the meaning for screen readers (createIcon renders
// <title> and aria-labelledby from it); the tooltip is a sighted convenience
// on top, never the mechanism.
export function PlatformLabel({ platform }: { platform: string }) {
  const Icon = PLATFORM_ICONS[platform]
  if (!Icon) return <>{platform}</>
  return (
    <Tooltip content={platform}>
      <Icon title={platform} />
    </Tooltip>
  )
}

export function PlatformList({ platforms }: { platforms: string[] }) {
  if (platforms.length === 0) return <>—</>
  return (
    <>
      {platforms.map((platform, index) => (
        <span key={platform} style={{ whiteSpace: 'nowrap' }}>
          {index > 0 && ' '}
          <PlatformLabel platform={platform} />
        </span>
      ))}
    </>
  )
}
