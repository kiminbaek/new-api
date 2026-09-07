import { beforeEach, describe, expect, test, vi } from 'vitest'

import { getPricing } from '../api'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/lib/api', () => ({ api: { get } }))

describe('pricing API', () => {
  beforeEach(() => get.mockReset())
  test('throws when HTTP 200 carries success false', async () => {
    get.mockResolvedValue({
      data: { success: false, message: 'pricing unavailable' },
    })
    await expect(getPricing()).rejects.toThrow('pricing unavailable')
  })
})
