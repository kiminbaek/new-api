/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'
import { requireSuccessfulResponse } from '@/lib/api-response'

export interface ProbeQualityRow {
  channel_id: number
  channel_name: string
  model: string
  total: number
  success: number
  failure: number
  success_rate: number
  failure_rate: number
  avg_latency_ms: number
  last_probe_at: number
  last_success: boolean
  last_error_category: string
  last_reason: string
  last_action: string
  consecutive_failures: number
  isolated: boolean
  isolation_level: string
}
export interface ProbeQualityTrend {
  date: string
  total: number
  success: number
  failure: number
}
export interface ProbeQualityCategory {
  category: string
  count: number
}
export interface ProbeQualityData {
  hours: number
  total: number
  success: number
  failure: number
  success_rate: number
  failure_rate: number
  affected_pairs: number
  isolated_pairs: number
  rows: ProbeQualityRow[]
  trend: ProbeQualityTrend[]
  categories: ProbeQualityCategory[]
}
export interface ProbeQualityEvent {
  id: number
  run_id: string
  channel_id: number
  channel_name: string
  model: string
  success: boolean
  latency_ms: number
  error_category: string
  error_code: string
  http_status: number
  reason: string
  suggestion: string
  action: string
  created_at: number
}

export async function getProbeQuality(
  hours: number
): Promise<ProbeQualityData> {
  const res = await api.get('/api/performance/probe-quality', {
    params: { hours },
  })
  return requireSuccessfulResponse(res.data, '探测质量数据加载失败').data
}

export async function getProbeQualityEvents(
  hours: number,
  channelId: number,
  model: string
): Promise<ProbeQualityEvent[]> {
  const res = await api.get('/api/performance/probe-quality/events', {
    params: { hours, channel_id: channelId, model, limit: 100 },
  })
  return requireSuccessfulResponse(res.data, '探测明细加载失败').data
}
