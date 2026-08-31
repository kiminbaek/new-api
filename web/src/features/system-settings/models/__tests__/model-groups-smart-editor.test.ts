import { describe, expect, test } from 'vitest'

import {
  appendGroupMembers,
  removeUnavailableMembers,
  splitMembers,
} from '../model-groups-utils'

describe('smart virtual model group helpers', () => {
  test('deduplicates manual and selected models while preserving order', () => {
    expect(appendGroupMembers('gpt-5, qwen-max', ['qwen-max', 'claude'])).toBe(
      'gpt-5, qwen-max, claude'
    )
  })

  test('removes only unavailable members after explicit cleanup', () => {
    expect(
      removeUnavailableMembers(
        'gpt-5, removed-model, qwen-max',
        new Set(['gpt-5', 'qwen-max'])
      )
    ).toBe('gpt-5, qwen-max')
  })

  test('accepts comma and newline separated manual input', () => {
    expect(splitMembers('gpt-5，qwen-max\nclaude、gpt-5')).toEqual([
      'gpt-5',
      'qwen-max',
      'claude',
    ])
  })
})
