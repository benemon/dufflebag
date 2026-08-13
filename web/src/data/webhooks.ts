import {
  platformDelete, platformGet, platformPatch, platformPost, type Tenant,
} from '../api/client'

export const WEBHOOK_OPERATIONS = [
  'version.created', 'version.completed', 'version.revoked', 'version.revocation_scheduled',
  'version.restored', 'version.deleted', 'channel.created', 'channel.deleted',
  'channel.assigned', 'bucket.created', 'bucket.deleted',
] as const

export type WebhookOperation = (typeof WEBHOOK_OPERATIONS)[number]

export type Webhook = {
  id: string
  name: string
  url: string
  description: string
  has_secret: boolean
  events: WebhookOperation[]
  state: 'pending' | 'active'
  last_verification_at: string | null
  last_verification_error: string | null
  created_at: string
  updated_at: string
}

export type WebhookWrite = {
  name: string
  url: string
  description?: string
  secret?: string
  events: WebhookOperation[]
}

export type WebhookDelivery = {
  id: string
  event_id: string
  operation: string
  status: 'pending' | 'retrying' | 'delivered' | 'failed' | 'refused'
  attempt_count: number
  first_attempted_at: string | null
  last_attempted_at: string | null
  response_code: number | null
  detail: string | null
  created_at: string
}

function path(tenant: Tenant, suffix = ''): string {
  const base = `/organizations/${encodeURIComponent(tenant.organizationID)}` +
    `/projects/${encodeURIComponent(tenant.projectID)}/webhooks`
  return suffix === '' ? base : `${base}/${suffix}`
}

export async function listWebhooks(token: string, tenant: Tenant): Promise<Webhook[]> {
  const body = await platformGet<{ webhooks?: Webhook[] }>(token, path(tenant))
  return body.webhooks ?? []
}

export function createWebhook(token: string, tenant: Tenant, write: WebhookWrite): Promise<Webhook> {
  return platformPost<Webhook>(token, path(tenant), write)
}

export function updateWebhook(
  token: string, tenant: Tenant, id: string, write: Partial<WebhookWrite>,
): Promise<Webhook> {
  return platformPatch<Webhook>(token, path(tenant, encodeURIComponent(id)), write)
}

export function verifyWebhook(token: string, tenant: Tenant, id: string): Promise<Webhook> {
  return platformPost<Webhook>(token, path(tenant, `${encodeURIComponent(id)}/verify`))
}

export async function deleteWebhook(token: string, tenant: Tenant, id: string): Promise<void> {
  await platformDelete(token, path(tenant, encodeURIComponent(id)))
}

export async function listWebhookDeliveries(
  token: string, tenant: Tenant, id: string,
): Promise<WebhookDelivery[]> {
  const body = await platformGet<{ deliveries?: WebhookDelivery[] }>(
    token, path(tenant, `${encodeURIComponent(id)}/deliveries`),
  )
  return body.deliveries ?? []
}
