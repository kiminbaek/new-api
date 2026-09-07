import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { updateSystemOption } from '../../api'
import { useUpdateOption } from '../use-update-option'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('../../api', () => ({ updateSystemOption: vi.fn() }))
const updateMock = vi.mocked(updateSystemOption)

function setup() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  const invalidate = vi
    .spyOn(queryClient, 'invalidateQueries')
    .mockResolvedValue(undefined)
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
  return {
    ...renderHook(() => useUpdateOption(), { wrapper }),
    invalidate,
    queryClient,
  }
}

describe('tool pricing cache policy', () => {
  beforeEach(() => vi.clearAllMocks())

  test('invalidates pricing and system options after tool price success', async () => {
    updateMock.mockResolvedValue({ success: true, message: '' })
    const { result, invalidate, queryClient } = setup()
    await act(async () => {
      await result.current.mutateAsync({
        key: 'tool_price_setting.prices',
        value: '{"web_search":1}',
      })
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['pricing'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['system-options'] })
    queryClient.clear()
  })

  test('does not invalidate caches after tool price failure', async () => {
    updateMock.mockRejectedValue(new Error('rejected'))
    const { result, invalidate, queryClient } = setup()
    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: 'tool_price_setting.prices',
          value: '{}',
        })
      ).rejects.toThrow('rejected')
    })
    expect(invalidate).not.toHaveBeenCalled()
    queryClient.clear()
  })
})
