import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  getFlowQuotaDates,
  getUptimeStatus,
  getUserQuotaDataByUsers,
  getUserQuotaDates,
} from './api'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/lib/api', () => ({ api: { get } }))

describe('dashboard API business failures', () => {
  beforeEach(() => get.mockReset())

  it.each([
    [
      'usage',
      () => getUserQuotaDates({ start_timestamp: 1, end_timestamp: 2 }),
    ],
    [
      'users',
      () => getUserQuotaDataByUsers({ start_timestamp: 1, end_timestamp: 2 }),
    ],
    ['uptime', () => getUptimeStatus()],
  ])('rejects HTTP 200 success:false for %s', async (_name, request) => {
    get.mockResolvedValue({
      data: { success: false, message: 'database unavailable' },
    })
    await expect(request()).rejects.toThrow('database unavailable')
  })

  it('leaves flow response validation to the flow state helper', async () => {
    get.mockResolvedValue({
      data: { success: false, message: 'flow unavailable' },
    })
    await expect(
      getFlowQuotaDates({ start_timestamp: 1, end_timestamp: 2 })
    ).resolves.toMatchObject({ success: false, message: 'flow unavailable' })
  })
})
