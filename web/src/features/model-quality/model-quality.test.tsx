import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import type { ModelQualityData } from './api'
import { ModelQuality } from './index'

const { getModelQuality } = vi.hoisted(() => ({ getModelQuality: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, getModelQuality }
})

const fixture: ModelQualityData = {
  hours: 168,
  request_count: 31,
  success_count: 29,
  success_rate: 93.548,
  models: [
    {
      model_name: 'quality-model-a',
      request_count: 30,
      success_count: 28,
      success_rate: 93.333,
      success_rate_excluding_rate_limit: 96.552,
      avg_latency_ms: 1200,
      p50_latency_ms: 900,
      p95_latency_ms: 2400,
      p50_ttft_ms: 180,
      p95_ttft_ms: 460,
      rate_limited: 1,
      channel_failures: 0,
      client_cancelled: 0,
      other_failures: 0,
      unclassified_failures: 1,
      failure_breakdown_coverage: false,
      quality_level: 'risk',
      probe_status: 'untested',
      health_score: 82,
      confidence: 0.75,
      route_count: 3,
      quarantined_routes: 1,
      retry_count: 2,
    },
    {
      model_name: 'quality-model-b',
      request_count: 1,
      success_count: 1,
      success_rate: 100,
      success_rate_excluding_rate_limit: 100,
      avg_latency_ms: 500,
      p50_latency_ms: 500,
      p95_latency_ms: 500,
      p50_ttft_ms: 100,
      p95_ttft_ms: 100,
      rate_limited: 0,
      channel_failures: 0,
      client_cancelled: 0,
      other_failures: 0,
      unclassified_failures: 0,
      failure_breakdown_coverage: true,
      quality_level: 'insufficient',
      probe_status: 'untested',
      health_score: 100,
      confidence: 0.1,
      route_count: 1,
      quarantined_routes: 0,
      retry_count: 0,
    },
  ],
  probe_dimensions: [
    { key: 'connectivity', label: '连通性', status: 'derived' },
    { key: 'reasoning', label: '回答合理性', status: 'untested' },
    { key: 'fingerprint', label: '模型指纹', status: 'untested' },
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
      <ModelQuality />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  getModelQuality.mockReset()
})

describe('ModelQuality', () => {
  test('renders truthful quality signals in desktop and mobile layouts', async () => {
    getModelQuality.mockResolvedValue(fixture)
    renderPage()

    expect(await screen.findByText('31')).toBeInTheDocument()
    expect(screen.getAllByText('quality-model-a')).toHaveLength(2)
    expect(screen.getAllByText('未分类历史 1')).toHaveLength(2)
    expect(screen.getByText('额外重试 2')).toBeInTheDocument()
    expect(screen.getByText(/2 次额外重试/)).toBeInTheDocument()
    expect(screen.getByText('流量派生')).toBeInTheDocument()
    expect(screen.getAllByText('未测')).toHaveLength(2)
  })

  test('filters the model table and mobile cards from one search input', async () => {
    getModelQuality.mockResolvedValue(fixture)
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('31')

    await user.type(
      screen.getByRole('textbox', { name: '搜索模型' }),
      'model-b'
    )

    expect(screen.queryByText('quality-model-a')).toBeNull()
    expect(screen.getAllByText('quality-model-b')).toHaveLength(2)
  })
})
