import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { resetModelRatios, updatePricingOptionsBulk } from '../../api'
import {
  usePricingOptionsMutation,
  useResetModelRatiosMutation,
} from '../use-pricing-options-mutation'

vi.mock('../../api', () => ({
  updatePricingOptionsBulk: vi.fn(),
  resetModelRatios: vi.fn(),
}))
const resetMock = vi.mocked(resetModelRatios)
const updateMock = vi.mocked(updatePricingOptionsBulk)

function setupHook<T>(hook: () => T) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  const invalidate = vi
    .spyOn(queryClient, 'invalidateQueries')
    .mockResolvedValue(undefined)
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
  return { ...renderHook(hook, { wrapper }), invalidate, queryClient }
}

describe('pricing mutation cache policy', () => {
  beforeEach(() => vi.clearAllMocks())
  test('invalidates pricing and system options only after success', async () => {
    updateMock.mockResolvedValue({ success: true, message: '' })
    const { result, invalidate, queryClient } = setupHook(
      usePricingOptionsMutation
    )
    await act(async () => {
      await result.current.mutateAsync({ values: { ModelRatio: '{}' } })
    })
    expect(invalidate).toHaveBeenCalledTimes(2)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['pricing'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['system-options'] })
    queryClient.clear()
  })
  test('does not invalidate either cache after failure', async () => {
    updateMock.mockRejectedValue(new Error('rejected'))
    const { result, invalidate, queryClient } = setupHook(
      usePricingOptionsMutation
    )
    await act(async () => {
      await expect(
        result.current.mutateAsync({ values: { ModelRatio: '{}' } })
      ).rejects.toThrow('rejected')
    })
    expect(invalidate).not.toHaveBeenCalled()
    queryClient.clear()
  })
  test('invalidates both caches only after reset succeeds', async () => {
    resetMock.mockResolvedValue({ success: true, message: '' })
    const { result, invalidate, queryClient } = setupHook(
      useResetModelRatiosMutation
    )
    await act(async () => {
      await result.current.mutateAsync()
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['pricing'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['system-options'] })
    queryClient.clear()
  })
  test('does not invalidate either cache after reset fails', async () => {
    resetMock.mockRejectedValue(new Error('reset rejected'))
    const { result, invalidate, queryClient } = setupHook(
      useResetModelRatiosMutation
    )
    await act(async () => {
      await expect(result.current.mutateAsync()).rejects.toThrow(
        'reset rejected'
      )
    })
    expect(invalidate).not.toHaveBeenCalled()
    queryClient.clear()
  })
})
