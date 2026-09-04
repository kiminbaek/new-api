import { describe, expect, test } from 'vitest'

import type { Channel } from '../../types'
import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
} from '../channel-form'

describe('channel concurrency form', () => {
  test('serializes explicit channel, key, and model limits', () => {
    const setting = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        max_concurrency: 8,
        max_concurrency_per_key: 2,
        concurrency_scope: 'redis',
        concurrency_group: 'same-account',
        model_concurrency_text: '{"gpt-5": 3, "*": 1}',
      })
    )

    expect(setting).toMatchObject({
      max_concurrency: 8,
      max_concurrency_per_key: 2,
      model_concurrency: { 'gpt-5': 3, '*': 1 },
      concurrency_scope: 'redis',
      concurrency_group: 'same-account',
    })
  })

  test('keeps disabled limits absent for backward compatibility', () => {
    const setting = JSON.parse(buildSettingJSON(CHANNEL_FORM_DEFAULT_VALUES))
    expect(setting).not.toHaveProperty('max_concurrency')
    expect(setting).not.toHaveProperty('max_concurrency_per_key')
    expect(setting).not.toHaveProperty('model_concurrency')
  })

  test('hydrates persisted limits when editing a channel', () => {
    const form = transformChannelToFormDefaults({
      id: 7,
      type: 1,
      key: '',
      name: 'limited',
      status: 1,
      models: 'gpt-5',
      group: 'default',
      priority: 0,
      weight: 0,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      base_url: '',
      other: '',
      balance: 0,
      balance_updated_time: 0,
      channel_info: { is_multi_key: false, multi_key_size: 0, multi_key_polling_index: 0, multi_key_mode: 'random' },
      settings: '{}',
      setting: '{"max_concurrency":8,"max_concurrency_per_key":2,"model_concurrency":{"gpt-5":3}}',
    } as Channel)

    expect(form.max_concurrency).toBe(8)
    expect(form.max_concurrency_per_key).toBe(2)
    expect(JSON.parse(form.model_concurrency_text || '{}')).toEqual({ 'gpt-5': 3 })
  })

  test('rejects malformed per-model JSON', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'limited',
      models: 'gpt-5',
      key: 'test',
      model_concurrency_text: 'not-json',
    })
    expect(result.success).toBe(false)
  })
})
