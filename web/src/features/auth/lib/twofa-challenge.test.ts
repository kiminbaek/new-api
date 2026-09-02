import { describe, expect, it } from 'vitest'

import { getTwoFAChallenge } from './twofa-challenge'

describe('getTwoFAChallenge', () => {
  it('accepts a valid challenge', () => {
    expect(
      getTwoFAChallenge({ require_2fa: true, flow_token: 'flow-1', expires_at: 42 })
    ).toEqual({ require_2fa: true, flow_token: 'flow-1', expires_at: 42 })
  })

  it.each([
    null,
    {},
    { require_2fa: false, flow_token: 'flow-1' },
    { require_2fa: true },
    { require_2fa: true, flow_token: '   ' },
    { access_token: 'token' },
  ])('rejects non-challenge payload %#', (value) => {
    expect(getTwoFAChallenge(value)).toBeNull()
  })
})
