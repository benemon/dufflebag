import { Alert, Button, Card, CardBody, Content, Spinner } from '@patternfly/react-core'

export function PluginLoadingCard({ message }: { message: string }) {
  return (
    <Card aria-busy="true">
      <CardBody style={{ padding: 64, textAlign: 'center' }}>
        <Spinner aria-label={message} />
        <Content component="p">{message}</Content>
      </CardBody>
    </Card>
  )
}

export function PluginErrorCard({ title, error, onRetry }: {
  title: string
  error: string
  onRetry: () => void | Promise<void>
}) {
  return (
    <Card>
      <CardBody>
        <Alert variant="danger" isInline title={title}>
          <Content component="p" style={{ fontFamily: 'monospace' }}>{error}</Content>
        </Alert>
        <Button variant="secondary" style={{ marginTop: 16 }} onClick={() => void onRetry()}>
          Retry
        </Button>
      </CardBody>
    </Card>
  )
}
