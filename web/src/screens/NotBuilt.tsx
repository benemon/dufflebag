import {
  Bullseye, EmptyState, EmptyStateBody, PageSection,
} from '@patternfly/react-core'

/** Placeholder for a screen the design covers but that is not implemented yet. */
export function NotBuilt({ title }: { title: string }) {
  return (
    <PageSection variant="secondary" isFilled>
      <Bullseye>
        <EmptyState titleText={title} headingLevel="h1">
          <EmptyStateBody>
            Not built yet. Manage this through the API or Terraform for now — the console
            will not show you something that is not really there.
          </EmptyStateBody>
        </EmptyState>
      </Bullseye>
    </PageSection>
  )
}
