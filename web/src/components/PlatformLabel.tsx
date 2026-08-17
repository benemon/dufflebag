import type { ComponentType } from 'react'
import { Tooltip } from '@patternfly/react-core'
import AwsIcon from '@patternfly/react-icons/dist/esm/icons/aws-icon'
import AzureIcon from '@patternfly/react-icons/dist/esm/icons/azure-icon'
import DockerIcon from '@patternfly/react-icons/dist/esm/icons/docker-icon'
import GoogleIcon from '@patternfly/react-icons/dist/esm/icons/google-icon'

// Docker and AWS are observed in real build metadata. Azure and GCP are
// recognised by the compatibility plane; their package-provided brand icons
// are mapped ahead of a live specimen. Unknown platforms still render below.
const PLATFORM_ICONS: Record<string, ComponentType<{ title?: string }>> = {
  aws: AwsIcon,
  azure: AzureIcon,
  docker: DockerIcon,
  gcp: GoogleIcon,
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
