import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { toast } from 'sonner'

import {
  createCustomOAuthProvider,
  deleteCustomOAuthProvider,
} from '../../api'
import {
  useCreateProvider,
  useDeleteProvider,
} from '../use-custom-oauth-mutations'

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('../../api', () => ({
  createCustomOAuthProvider: vi.fn(),
  updateCustomOAuthProvider: vi.fn(),
  deleteCustomOAuthProvider: vi.fn(),
  discoverOIDCEndpoints: vi.fn(),
}))

const createProviderMock = vi.mocked(createCustomOAuthProvider)
const deleteProviderMock = vi.mocked(deleteCustomOAuthProvider)

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return function Wrapper(props: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        {props.children}
      </QueryClientProvider>
    )
  }
}

describe('custom OAuth mutations', () => {
  beforeEach(() => vi.clearAllMocks())

  it('rejects a business failure and does not report create success', async () => {
    createProviderMock.mockResolvedValue({
      success: false,
      message: 'duplicate slug',
    })
    const { result } = renderHook(() => useCreateProvider(), {
      wrapper: createWrapper(),
    })

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          name: 'Example',
          slug: 'example',
          icon: '',
          enabled: true,
          client_id: 'client',
          client_secret: 'secret',
          authorization_endpoint: 'https://id.example/authorize',
          token_endpoint: 'https://id.example/token',
          user_info_endpoint: 'https://id.example/userinfo',
          scopes: 'openid',
          user_id_field: 'sub',
          username_field: '',
          display_name_field: '',
          email_field: '',
          well_known: '',
          auth_style: 0,
          access_policy: '',
          access_denied_message: '',
        })
      ).rejects.toThrow('duplicate slug')
    })

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('duplicate slug'))
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('rejects a failed delete so callers can keep confirmation open', async () => {
    deleteProviderMock.mockResolvedValue({
      success: false,
      message: 'provider is in use',
    })
    const { result } = renderHook(() => useDeleteProvider(), {
      wrapper: createWrapper(),
    })

    await act(async () => {
      await expect(result.current.mutateAsync(7)).rejects.toThrow(
        'provider is in use'
      )
    })

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('provider is in use')
    )
    expect(toast.success).not.toHaveBeenCalled()
  })
})
