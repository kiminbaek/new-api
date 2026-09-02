import { describe, expect, it } from 'vitest'

import { requireSuccessfulResponse } from './api-response'

describe('requireSuccessfulResponse', () => {
  it('returns a successful response unchanged', () => {
    const response = { success: true, data: [1, 2, 3] }
    expect(requireSuccessfulResponse(response, 'fallback')).toBe(response)
  })

  it('throws the server message for a business failure', () => {
    expect(() =>
      requireSuccessfulResponse(
        { success: false, message: 'permission denied' },
        'fallback'
      )
    ).toThrow('permission denied')
  })

  it('uses the fallback when the server omits a message', () => {
    expect(() =>
      requireSuccessfulResponse({ success: false }, 'load failed')
    ).toThrow('load failed')
  })
})
