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
}

export async function getModelPriority(): Promise<ModelPriorityRow[]> {
  const res = await api.get('/api/channel/model-priority')
  return res.data.data || []
}
