import { beforeEach, describe, expect, test, vi } from 'vitest'

import { resetModelRatios, updatePricingOptionsBulk } from '../api'

const { put, post } = vi.hoisted(() => ({ put: vi.fn(), post: vi.fn() }))
vi.mock('@/lib/api', () => ({ api: { put, post } }))

describe('pricing settings API', () => {
  beforeEach(() => {
    put.mockReset()
    post.mockReset()
  })

  test('submits the complete mapping in one bulk request', async () => {
    put.mockResolvedValue({ data: { success: true, message: '' } })
    const request = {
      values: { ModelRatio: '{\"m\":1}', CacheRatio: '{\"m\":0.5}' },
    }
    await expect(updatePricingOptionsBulk(request)).resolves.toMatchObject({
      success: true,
    })
    expect(put).toHaveBeenCalledTimes(1)
    expect(put).toHaveBeenCalledWith('/api/option/pricing/bulk', request)
  })

  test('throws HTTP 200 business failures for bulk save and reset', async () => {
    put.mockResolvedValue({
      data: { success: false, message: 'bulk rejected' },
    })
    post.mockResolvedValue({
      data: { success: false, message: 'reset rejected' },
    })
    await expect(
      updatePricingOptionsBulk({ values: { ModelRatio: '{}' } })
    ).rejects.toThrow('bulk rejected')
    await expect(resetModelRatios()).rejects.toThrow('reset rejected')
  })
})
