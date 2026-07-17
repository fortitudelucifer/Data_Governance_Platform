import { client } from './client'

export interface CapabilityProvider {
  id: number
  name: string
  type: string
  capability_type: string
  provider_kind: string
  endpoint: string
  api_key?: string
  model?: string
  extra_config?: string
  enabled: boolean
  timeout_seconds: number
  max_retries: number
  priority: number
  last_test_success?: boolean | null
  last_test_at?: string | null
  last_test_latency_ms?: number | null
}

export interface CapabilityTypeMeta {
  capability_type: string
  label: string
  provider_kinds: string[]
  required_fields: string[]
}

export interface TestResult {
  success: boolean
  latency_ms: number
  error?: string
}

export interface ProbeRequest {
  endpoint: string
  provider_kind: string
  api_key?: string
}

// 环境变量注册的适配器（只读）——启动时按 MM_*_ENDPOINT 接入的本地/云端模型，
// 用于在能力配置中展示“已连通但非 DB 管理”的能力。
export interface EnvAdapter {
  capability_type: string
  provider_name: string
  provider_kind: string
  endpoint: string
  model?: string
}

export const capabilityApi = {
  listTypes: () => client.get<CapabilityTypeMeta[]>('/capabilities/types').then((r) => r.data),

  listProviders: (capabilityType?: string) =>
    client.get<CapabilityProvider[]>('/capabilities/providers', {
      params: capabilityType ? { capability_type: capabilityType } : undefined,
    }).then((r) => r.data),

  listEnvAdapters: () =>
    client.get<EnvAdapter[]>('/capabilities/providers/env').then((r) => r.data),

  get: (id: number) => client.get<CapabilityProvider>(`/capabilities/providers/${id}`).then((r) => r.data),

  create: (data: Partial<CapabilityProvider>) =>
    client.post<CapabilityProvider>('/capabilities/providers', data).then((r) => r.data),

  update: (id: number, data: Partial<CapabilityProvider>) =>
    client.put(`/capabilities/providers/${id}`, data).then((r) => r.data),

  delete: (id: number) => client.delete(`/capabilities/providers/${id}`),

  test: (id: number) => client.post<TestResult>(`/capabilities/providers/${id}/test`).then((r) => r.data),

  probe: (data: ProbeRequest) => client.post<TestResult>('/capabilities/providers/probe', data).then((r) => r.data),
}
