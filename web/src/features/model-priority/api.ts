import { api } from '@/lib/api'

export interface ModelPriorityRow {
  channel_id: number
  channel_name: string
  model: string
  group: string
  enabled: boolean
  base_priority: number
  eff_priority: number
  delta: number
  weight: number
  health_score: number
  confidence: number
  routing_status: 'observe' | 'healthy' | 'canary' | 'quarantined'
  canary_percent: number
  canary_stage?: number
  reason?: string
  disabled_at?: number
  next_probe_at?: number
  attempts?: number
  probing?: boolean
  attribution?: {
    category: string
    confidence: number
    action: string
    summary: string
  }
}

export async function getModelPriority(): Promise<ModelPriorityRow[]> {
  const res = await api.get('/api/channel/model-priority')
  if (!res.data?.success) {
    throw new Error(res.data?.message || '模型优先级数据加载失败')
  }
  return res.data.data || []
}
