import { describe, expect, test } from 'vitest'

import type { Channel } from '../../types'
import {
  buildSettingJSON,
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  hasNonDefaultReliabilitySettings,
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
  updateOpenAIPythonFingerprint,
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
      channel_info: {
        is_multi_key: false,
        multi_key_size: 0,
        multi_key_polling_index: 0,
        multi_key_mode: 'random',
      },
      settings: '{}',
      setting:
        '{"max_concurrency":8,"max_concurrency_per_key":2,"model_concurrency":{"gpt-5":3}}',
    } as Channel)

    expect(form.max_concurrency).toBe(8)
    expect(form.max_concurrency_per_key).toBe(2)
    expect(JSON.parse(form.model_concurrency_text || '{}')).toEqual({
      'gpt-5': 3,
    })
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

describe('渠道新建默认配置', () => {
  test('默认启用 180 秒超时、3 次失败阈值和 OpenAI Python 指纹', () => {
    expect(CHANNEL_FORM_DEFAULT_VALUES.timeout_seconds).toBe(180)
    expect(CHANNEL_FORM_DEFAULT_VALUES.fail_threshold).toBe(3)
    expect(CHANNEL_FORM_DEFAULT_VALUES.openai_python_fingerprint_enabled).toBe(
      true
    )
    const headers = JSON.parse(
      CHANNEL_FORM_DEFAULT_VALUES.header_override || '{}'
    )
    expect(headers['User-Agent']).toBe('OpenAI/Python 2.33.0')
    expect(headers['X-Stainless-Runtime']).toBe('CPython')
    const setting = JSON.parse(buildSettingJSON(CHANNEL_FORM_DEFAULT_VALUES))
    expect(setting.timeout_seconds).toBe(180)
    expect(setting.fail_threshold).toBe(3)
    expect(setting.openai_python_fingerprint_enabled).toBe(true)
  })

  test('关闭指纹只删除系统生成值并保留自定义请求头', () => {
    const headers = JSON.parse(
      updateOpenAIPythonFingerprint(
        JSON.stringify({
          ...JSON.parse(CHANNEL_FORM_DEFAULT_VALUES.header_override || '{}'),
          'User-Agent': '我的自定义客户端',
          'X-Custom': '保留',
        }),
        false
      ) || '{}'
    )
    expect(headers['User-Agent']).toBe('我的自定义客户端')
    expect(headers['X-Custom']).toBe('保留')
    expect(headers['X-Stainless-Runtime']).toBeUndefined()
  })

  test('重新开启指纹补齐九头但不覆盖自定义同名值', () => {
    const headers = JSON.parse(
      updateOpenAIPythonFingerprint(
        JSON.stringify({ 'User-Agent': '我的自定义客户端' }),
        true
      )
    )
    expect(headers['User-Agent']).toBe('我的自定义客户端')
    expect(headers['X-Stainless-Runtime']).toBe('CPython')
    expect(Object.keys(headers)).toHaveLength(9)
  })
})

describe('渠道设置无损保存', () => {
  test('编辑旧渠道时保留前端不认识的 setting 字段', () => {
    const setting = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        setting: JSON.stringify({
          future_plugin_option: { enabled: true },
          timeout_seconds: 30,
        }),
        timeout_seconds: 45,
      })
    )
    expect(setting.future_plugin_option).toEqual({ enabled: true })
    expect(setting.timeout_seconds).toBe(45)
  })
})

describe('请求指纹大小写与更新 payload', () => {
  test('已有小写同名 Header 时不重复添加且保留用户值', () => {
    const headers = JSON.parse(
      updateOpenAIPythonFingerprint(
        JSON.stringify({
          'user-agent': '我的小写客户端',
          accept: 'text/plain',
        }),
        true
      )
    )
    expect(headers['user-agent']).toBe('我的小写客户端')
    expect(headers.accept).toBe('text/plain')
    expect(headers['User-Agent']).toBeUndefined()
    expect(headers.Accept).toBeUndefined()
    expect(Object.keys(headers)).toHaveLength(9)
  })

  test('更新 payload 使用开关规范化后的 Header', () => {
    const payload = transformFormDataToUpdatePayload(
      {
        ...CHANNEL_FORM_DEFAULT_VALUES,
        header_override: JSON.stringify({ 'X-Custom': '保留' }),
        openai_python_fingerprint_enabled: true,
      },
      42
    )
    const headers = JSON.parse(payload.header_override || '{}')
    expect(headers['X-Custom']).toBe('保留')
    expect(headers['X-Stainless-Runtime']).toBe('CPython')
  })
})

describe('请求头名称唯一性', () => {
  test('拒绝仅大小写不同的重复 Header 名', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      header_override: JSON.stringify({
        'User-Agent': 'OpenAI/Python 2.33.0',
        'user-agent': 'custom-client',
      }),
    })
    expect(result.success).toBe(false)
  })
})

describe('可靠性默认值的高级配置状态', () => {
  test('系统默认 180/3/九头不算用户已配置高级项', () => {
    expect(hasNonDefaultReliabilitySettings(CHANNEL_FORM_DEFAULT_VALUES)).toBe(
      false
    )
  })

  test('偏离默认值或增加自定义 Header 时算高级项', () => {
    expect(
      hasNonDefaultReliabilitySettings({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        timeout_seconds: 30,
      })
    ).toBe(true)
    expect(
      hasNonDefaultReliabilitySettings({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        header_override: JSON.stringify({
          ...JSON.parse(CHANNEL_FORM_DEFAULT_VALUES.header_override || '{}'),
          'X-Custom': '保留',
        }),
      })
    ).toBe(true)
  })
})
