import { Skeleton } from '@patternfly/react-core'

export function SkeletonRows({ screenreaderText }: { screenreaderText: string }) {
  return (
    <div aria-busy="true" style={{ display: 'grid', gap: 12 }}>
      {[100, 88, 94, 72].map((width, index) => (
        <Skeleton
          key={width}
          width={`${width}%`}
          screenreaderText={index === 0 ? screenreaderText : undefined}
        />
      ))}
    </div>
  )
}
