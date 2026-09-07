import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { PanelWrapper } from './panel-wrapper'

describe('PanelWrapper', () => {
  test('keeps header actions available while loading', () => {
    render(
      <PanelWrapper
        title='Uptime'
        loading
        headerActions={<button type='button'>Refresh</button>}
      />
    )

    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()
  })

  test('keeps header actions available in the empty state', () => {
    render(
      <PanelWrapper
        title='Uptime'
        empty
        emptyMessage='No monitors'
        headerActions={<button type='button'>Refresh</button>}
      >
        <div>content</div>
      </PanelWrapper>
    )

    expect(screen.getByText('No monitors')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()
  })
})
