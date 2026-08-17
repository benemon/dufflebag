import {
  Button, EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter, PageSection,
} from '@patternfly/react-core'
import { useNavigate } from 'react-router'

export function Registry() {
  const navigate = useNavigate()
  return (
    <PageSection variant="secondary" isFilled>
      <RegistryView onConnectClient={() => navigate('/instance')} />
    </PageSection>
  )
}

export function RegistryView({ onConnectClient }: { onConnectClient: () => void }) {
  return (
    <EmptyState titleText="Choose a bucket" headingLevel="h2">
      <EmptyStateBody>
        Pick a bucket from the masthead picker, or create one there.
      </EmptyStateBody>
      <EmptyStateFooter>
        <EmptyStateActions>
          <Button variant="primary" onClick={onConnectClient}>Connect a client</Button>
        </EmptyStateActions>
        <EmptyStateActions>
          <Button
            component="a"
            href="https://developer.hashicorp.com/packer/docs"
            target="_blank"
            rel="noreferrer"
            variant="link"
          >
            Packer docs
          </Button>
        </EmptyStateActions>
      </EmptyStateFooter>
    </EmptyState>
  )
}
