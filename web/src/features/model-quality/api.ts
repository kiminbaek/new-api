import { api } from '@/lib/api'
import { requireSuccessfulResponse } from '@/lib/api-response'

export type ProbeResultStatus = 'pass' | 'fail' | 'error' | 'untested'
export type ProbeTaskStatus = 'pending' | 'running' | 'succeeded' | 'failed'
export interface ProbeResult {
  id: number
  run_id: string
  task_id: string
  model: string
  dimension: string
  status: ProbeResultStatus
  score: number | null
  evidence: string
  latency_ms: number
  created_at: number
}
export interface ProbeTask {
  task_id: string
  status: ProbeTaskStatus
  error?: string
}
export interface ProbeTaskResponse {
  success: boolean
  message?: string
  created?: boolean
  data: ProbeTask
}

export type QualityLevel =
  | 'stable'
  | 'fluctuating'
  | 'risk'
  | 'insufficient'
  | 'untested'
export interface ModelQualityRow {
  model_name: string
  request_count: number
  success_count: number
  success_rate: number
  success_rate_excluding_rate_limit: number
  avg_latency_ms: number
  p50_latency_ms: number
  p95_latency_ms: number
  p50_ttft_ms: number
  p95_ttft_ms: number
  rate_limited: number
  channel_failures: number
  client_cancelled: number
  other_failures: number
  unclassified_failures: number
  failure_breakdown_coverage: boolean
  quality_level: QualityLevel
  probe_status: ProbeResultStatus
  probe_results: ProbeResult[]
  last_probe_at: number
  health_score: number | null
  confidence: number
  latency_sample_count: number
  route_count: number
  quarantined_routes: number
  retry_count: number
}
export interface ProbeDimension {
  key: string
  label: string
  description: string
  source: 'derived' | 'active'
}
export interface ModelQualityData {
  hours: number
  request_count: number
  success_count: number
  success_rate: number
  models: ModelQualityRow[]
  probe_dimensions: ProbeDimension[]
  probe_auto_enabled: boolean
  probe_routing_impact: false
}
export async function getModelQuality(
  hours: number
): Promise<ModelQualityData> {
  const res = await api.get('/api/performance/model-quality', {
    params: { hours },
  })
  return requireSuccessfulResponse(res.data, '模型质量数据加载失败').data
}

export async function startModelQualityProbe(
  models?: string[]
): Promise<ProbeTaskResponse> {
  const res = await api.post<ProbeTaskResponse>(
    '/api/system-task/model-quality-probe',
    { models: models ?? [] }
  )
  return requireSuccessfulResponse(res.data, '主动探针启动失败')
}

export async function getModelQualityProbeTask(
  taskId: string
): Promise<ProbeTask> {
  const res = await api.get<ProbeTaskResponse>(`/api/system-task/${taskId}`, {
    skipBusinessError: true,
    skipErrorHandler: true,
  })
  return requireSuccessfulResponse(res.data, '主动探针状态加载失败').data
}
