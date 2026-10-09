import { platformGet, platformPost } from '../api/client'

export type PluginRegistry = {
  enabled: boolean
  exposed: boolean
}

function path(organizationID: string, action = ''): string {
  const base = `/organizations/${encodeURIComponent(organizationID)}/plugin-registry`
  return action === '' ? base : `${base}/${action}`
}

export function getPluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformGet<PluginRegistry>(token, path(organizationID))
}

export function enablePluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformPost<PluginRegistry>(token, path(organizationID, 'enable'))
}

export function exposePluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformPost<PluginRegistry>(token, path(organizationID, 'expose'))
}

export function unexposePluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformPost<PluginRegistry>(token, path(organizationID, 'unexpose'))
}

export async function disablePluginRegistry(token: string, organizationID: string): Promise<void> {
  await platformPost<null>(token, path(organizationID, 'disable'))
}
