import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { DataTablePage } from '../data-table-page'

describe('DataTablePage error state', () => {
  it('keeps a retry path without rendering empty data or pagination', () => {
    const onRetry = vi.fn()

    render(
      <DataTablePage
        table={{} as never}
        columns={[]}
        error={new Error('channels unavailable')}
        onRetry={onRetry}
        emptyTitle='No Channels Found'
        showPagination
      />
    )

    expect(screen.getByText('channels unavailable')).toBeInTheDocument()
    expect(screen.queryByText('No Channels Found')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledOnce()
  })
})
