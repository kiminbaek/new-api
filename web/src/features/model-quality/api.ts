import { api } from '@/lib/api'
import { requireSuccessfulResponse } from '@/lib/api-response'

export type QualityLevel = 'stable' | 'fluctuating' | 'risk' | 'insufficient'
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
  probe_status: 'untested'
  health_score: number
  confidence: number
  route_count: number
  quarantined_routes: number
  retry_count: number
}
export interface ProbeDimension {
  key: string
  label: string
  status: 'derived' | 'untested'
}
export interface ModelQualityData {
  hours: number
  request_count: number
  success_count: number
  success_rate: number
  models: ModelQualityRow[]
  probe_dimensions: ProbeDimension[]
}
export async function getModelQuality(
  hours: number
): Promise<ModelQualityData> {
  const res = await api.get('/api/performance/model-quality', {
    params: { hours },
  })
  return requireSuccessfulResponse(res.data, '模型质量数据加载失败').data
}
