import { describe, expect, it } from 'vitest'

import { taskPluginMutationResult } from '../api'

describe('taskPluginMutationResult', () => {
  it('marks a committed mutation whose runtime sync is pending', () => {
    expect(
      taskPluginMutationResult({
        data: { success: true, message: '', data: { saved: true } },
        headers: { 'x-task-plugin-runtime-sync': 'pending' },
      })
    ).toEqual({ data: { saved: true }, runtimeSyncPending: true })
  })

  it('keeps an ordinary successful mutation as synchronized', () => {
    expect(
      taskPluginMutationResult({
        data: { success: true, message: '', data: null },
        headers: {},
      })
    ).toEqual({ data: null, runtimeSyncPending: false })
  })
})
