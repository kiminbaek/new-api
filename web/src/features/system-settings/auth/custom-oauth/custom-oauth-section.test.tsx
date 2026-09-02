import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { CustomOAuthSection } from './custom-oauth-section'
import { useCustomOAuthProviders } from './hooks/use-custom-oauth-providers'

vi.mock('./hooks/use-custom-oauth-providers', () => ({
  useCustomOAuthProviders: vi.fn(),
}))
vi.mock('./components/provider-table', () => ({
  ProviderTable: () => <div>No custom OAuth providers configured yet.</div>,
}))
vi.mock('./components/provider-form-dialog', () => ({
  ProviderFormDialog: () => null,
}))

const providersQueryMock = vi.mocked(useCustomOAuthProviders)

describe('CustomOAuthSection', () => {
  it('renders a retryable error instead of the normal empty state', () => {
    const refetch = vi.fn()
    providersQueryMock.mockReturnValue({
      data: undefined,
      error: new Error('provider service unavailable'),
      isError: true,
      isFetching: false,
      isLoading: false,
      refetch,
    } as unknown as ReturnType<typeof useCustomOAuthProviders>)

    render(<CustomOAuthSection serverAddress='https://example.com' />)

    expect(
      screen.getByText('Unable to load custom OAuth providers')
    ).toBeInTheDocument()
    expect(screen.getByText('provider service unavailable')).toBeInTheDocument()
    expect(
      screen.queryByText('No custom OAuth providers configured yet.')
    ).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(refetch).toHaveBeenCalledOnce()
  })
})
