import { api } from './client'
import type { HealthResponse, PublicConfig, VersionInfo } from '@/types/api'

/**
 * System endpoints: health, version, and the public configuration the browser
 * needs to render correctly. None of these require authentication.
 */

/** `GET /health` — service and dependency status. */
export function fetchHealth(signal?: AbortSignal): Promise<HealthResponse> {
  // /health lives outside /api/v1 because it is an infrastructure contract
  // that must stay stable across API versions.
  return api.get<HealthResponse>('/health', signal ? { signal } : {})
}

/** `GET /api/v1/version` — build metadata. */
export function fetchVersion(signal?: AbortSignal): Promise<VersionInfo> {
  return api.get<VersionInfo>('version', signal ? { signal } : {})
}

/** `GET /api/v1/config` — client-relevant settings. */
export function fetchPublicConfig(signal?: AbortSignal): Promise<PublicConfig> {
  return api.get<PublicConfig>('config', signal ? { signal } : {})
}

/** React Query keys, kept together so cache invalidation stays consistent. */
export const systemKeys = {
  all: ['system'] as const,
  health: () => [...systemKeys.all, 'health'] as const,
  version: () => [...systemKeys.all, 'version'] as const,
  config: () => [...systemKeys.all, 'config'] as const,
}
