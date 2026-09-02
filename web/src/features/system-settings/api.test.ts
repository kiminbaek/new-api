import { beforeEach, describe, expect, it, vi } from 'vitest'

import { getSystemOptions, updateSystemOption } from './api'

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}))

vi.mock('@/lib/api', () => ({ api: { get, put } }))

describe('system settings API contracts', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('rejects a business failure while loading settings', async () => {
    get.mockResolvedValue({
      data: { success: false, message: 'load denied', data: [] },
    })
    await expect(getSystemOptions()).rejects.toThrow('load denied')
  })

  it('rejects a business failure while saving a setting', async () => {
    put.mockResolvedValue({
      data: { success: false, message: 'save rejected' },
    })
    await expect(
      updateSystemOption({ key: 'ModelGroups', value: '{}' })
    ).rejects.toThrow('save rejected')
  })
})
