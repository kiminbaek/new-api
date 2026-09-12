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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import type { ProbeQualityData } from './api'
import { EventList, ProbeQuality } from './index'

const { getProbeQuality, getProbeQualityEvents } = vi.hoisted(() => ({
  getProbeQuality: vi.fn(),
  getProbeQualityEvents: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, getProbeQuality, getProbeQualityEvents }
})

const fixture: ProbeQualityData = {
  hours: 168,
  total: 12,
  success: 9,
  failure: 3,
  success_rate: 75,
  failure_rate: 25,
  affected_pairs: 1,
  isolated_pairs: 1,
  trend: [{ date: '2026-09-12', total: 12, success: 9, failure: 3 }],
  categories: [{ category: 'account_quota', count: 3 }],
  rows: [
    {
      channel_id: 53,
      channel_name: '小学生公益站',
      model: 'glm-5.3-flash',
      total: 10,
      success: 7,
      failure: 3,
      success_rate: 70,
      failure_rate: 30,
      avg_latency_ms: 1250,
      last_probe_at: 1789185600,
      last_success: false,
      last_error_category: 'account_quota',
      last_reason: '当前模型对应的上游账号池额度不足',
      last_action: 'observe',
      consecutive_failures: 3,
      isolated: true,
      isolation_level: 'model',
    },
    {
      channel_id: 45,
      channel_name: 'TokenBom',
      model: 'glm-5.3-flash',
      total: 2,
      success: 2,
      failure: 0,
      success_rate: 100,
      failure_rate: 0,
      avg_latency_ms: 800,
      last_probe_at: 1789185600,
      last_success: true,
      last_error_category: '',
      last_reason: '',
      last_action: 'pass',
      consecutive_failures: 0,
      isolated: false,
      isolation_level: '',
    },
  ],
}

const clients: QueryClient[] = []
function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ProbeQuality />
    </QueryClientProvider>
  )
}
afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  getProbeQuality.mockReset()
  getProbeQualityEvents.mockReset()
})

describe('ProbeQuality', () => {
  test('shows seven-day channel-model failure truth in desktop and mobile views', async () => {
    getProbeQuality.mockResolvedValue(fixture)
    renderPage()
    expect(await screen.findByText('12')).toBeInTheDocument()
    expect(getProbeQuality).toHaveBeenCalledWith(168)
    expect(screen.getByText('25.0%')).toBeInTheDocument()
    expect(screen.getAllByText('小学生公益站')).toHaveLength(2)
    expect(screen.getAllByText('#53')).toHaveLength(2)
    expect(screen.getAllByText('额度 / 预算池').length).toBeGreaterThan(0)
    expect(screen.getAllByText('已隔离')).toHaveLength(2)
  })

  test('filters failed pairs and opens persisted event details', async () => {
    getProbeQuality.mockResolvedValue(fixture)
    getProbeQualityEvents.mockResolvedValue([
      {
        id: 1,
        run_id: 'r1',
        channel_id: 53,
        channel_name: '小学生公益站',
        model: 'glm-5.3-flash',
        success: false,
        latency_ms: 1234,
        error_category: 'account_quota',
        error_code: 'insufficient_user_quota',
        http_status: 400,
        reason: '当前模型对应的上游账号池额度不足',
        suggestion: '该请求会切换备用渠道',
        action: 'observe',
        created_at: 1789185600,
      },
    ])
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('12')
    await user.click(screen.getByRole('button', { name: '仅失败' }))
    expect(screen.queryByText('TokenBom #45')).toBeNull()
    await user.click(screen.getAllByRole('button', { name: '查看' })[0])
    await waitFor(() =>
      expect(getProbeQualityEvents).toHaveBeenCalledWith(
        168,
        53,
        'glm-5.3-flash'
      )
    )
  })

  test('renders persisted event reason, status and action', () => {
    render(
      <EventList
        events={[
          {
            id: 1,
            run_id: 'r1',
            channel_id: 53,
            channel_name: '小学生公益站',
            model: 'glm-5.3-flash',
            success: false,
            latency_ms: 1234,
            error_category: 'account_quota',
            error_code: 'insufficient_user_quota',
            http_status: 400,
            reason: '当前模型对应的上游账号池额度不足',
            suggestion: '该请求会切换备用渠道',
            action: 'observe',
            created_at: 1789185600,
          },
        ]}
      />
    )
    expect(screen.getByText('HTTP 400')).toBeInTheDocument()
    expect(screen.getByText('insufficient_user_quota')).toBeInTheDocument()
    expect(screen.getByText('动作：观察 / 降权')).toBeInTheDocument()
    expect(
      screen.getByText('当前模型对应的上游账号池额度不足')
    ).toBeInTheDocument()
  })

  test('shows a truthful first-run empty state', async () => {
    getProbeQuality.mockResolvedValue({
      ...fixture,
      total: 0,
      success: 0,
      failure: 0,
      success_rate: 0,
      failure_rate: 0,
      affected_pairs: 0,
      isolated_pairs: 0,
      rows: [],
      trend: [],
      categories: [],
    })
    renderPage()
    expect(
      await screen.findByText('尚无历史数据；下一轮逐模型巡检完成后自动显示')
    ).toBeInTheDocument()
    expect(
      screen.getByText('尚无探测历史；下一轮巡检完成后开始累计')
    ).toBeInTheDocument()
  })
})
