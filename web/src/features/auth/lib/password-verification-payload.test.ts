import { describe, expect, it, vi } from 'vitest'

import { buildPasswordVerificationPayload } from './password-verification-payload'

describe('buildPasswordVerificationPayload', () => {
  it('preserves the exact password when transport encryption is disabled', async () => {
    const encrypt = vi.fn()
    await expect(
      buildPasswordVerificationPayload(
        '  exact password  ',
        { enabled: false },
        encrypt
      )
    ).resolves.toEqual({ password: '  exact password  ' })
    expect(encrypt).not.toHaveBeenCalled()
  })

  it('rejects an enabled capability without a complete public key', async () => {
    await expect(
      buildPasswordVerificationPayload(
        'password',
        { enabled: true, kid: 'key-1' },
        vi.fn()
      )
    ).rejects.toThrow('Password encryption capability is incomplete')
  })

  it('returns only encrypted password fields when encryption is enabled', async () => {
    const encrypt = vi.fn().mockResolvedValue('ciphertext')
    await expect(
      buildPasswordVerificationPayload(
        'password',
        { enabled: true, kid: 'key-1', public_key: 'public-key' },
        encrypt
      )
    ).resolves.toEqual({
      password_encrypted: 'ciphertext',
      encryption_key_id: 'key-1',
    })
    expect(encrypt).toHaveBeenCalledWith('password', 'public-key')
  })
})
