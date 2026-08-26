/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
// [CUSTOM] 哨兵推送设置卡片：渠道/模型异常事件主动通知（QQ 网关 webhook + 可选邮件）。
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const sentinelFormSchema = z.object({
  SentinelEnabled: z.boolean(),
  SentinelWebhookURL: z.string(),
  SentinelWebhookAuth: z.string(),
  SentinelEmailTo: z.string(),
  SentinelDailyHour: z.coerce.number().int().min(0).max(23),
})

type SentinelFormValues = z.infer<typeof sentinelFormSchema>

interface SentinelSectionProps {
  defaultValues: Record<string, string | undefined>
}

export function SentinelSection({ defaultValues }: SentinelSectionProps) {
  const t = useTranslation()
  const updateOption = useUpdateOption()
  const baselineRef = useRef<Partial<SentinelFormValues>>({})

  const form = useForm<SentinelFormValues>({
    resolver: zodResolver(sentinelFormSchema),
    defaultValues: {
      SentinelEnabled: false,
      SentinelWebhookURL: '',
      SentinelWebhookAuth: '',
      SentinelEmailTo: '',
      SentinelDailyHour: 8,
    },
  })

  useEffect(() => {
    const values: SentinelFormValues = {
      SentinelEnabled: defaultValues['SentinelEnabled'] === 'true',
      SentinelWebhookURL: defaultValues['SentinelWebhookURL'] ?? '',
      SentinelWebhookAuth: defaultValues['SentinelWebhookAuth'] ?? '',
      SentinelEmailTo: defaultValues['SentinelEmailTo'] ?? '',
      SentinelDailyHour: Number(defaultValues['SentinelDailyHour'] ?? 8) || 8,
    }
    form.reset(values)
    baselineRef.current = values
  }, [defaultValues, form])

  const onSubmit = async (values: SentinelFormValues) => {
    const updates = (
      Object.keys(values) as Array<keyof SentinelFormValues>
    ).filter((key) => String(values[key]) !== String(baselineRef.current[key]))

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const key of updates) {
      await updateOption.mutateAsync({
        key,
        value: String(values[key]),
      })
    }

    baselineRef.current = values
    toast.success(t('Settings saved'))
  }

  const testPush = async () => {
    try {
      const res = await fetch('/api/sentinel/test', { method: 'POST' })
      const data = await res.json()
      if (data.success) {
        toast.success(t('Test notification sent — check your channel'))
      } else {
        toast.error(data.message || t('Test failed'))
      }
    } catch {
      toast.error(t('Request failed'))
    }
  }

  return (
    <SettingsSection title={t('Sentinel Push')}>
      <p className='text-muted-foreground mb-2 text-sm'>
        {t(
          'Automatically push notifications for channel/model anomalies (auto-disable, recovery, suspected model removal, low redundancy). Configure the QQ Gateway address below and it works once your bot is online.',
        )}
      </p>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsSwitchContent>
            <FormField
              control={form.control}
              name='SentinelEnabled'
              render={({ field }) => (
                <SettingsSwitchItem
                  label={t('Enable sentinel push')}
                  description={t(
                    'Off or unconfigured channels = fully silent, never blocks the main flow',
                  )}
                >
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
          </SettingsSwitchContent>

          <FormField
            control={form.control}
            name='SentinelWebhookURL'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Webhook URL')}</FormLabel>
                <FormControl>
                  <Input placeholder='http://127.0.0.1:3018/api/webui/send' {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Generic JSON webhook. Fill in the QQ Gateway send endpoint to receive pushes in QQ.',
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='SentinelWebhookAuth'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Webhook Token')}</FormLabel>
                <FormControl>
                  <Input type='password' placeholder='Bearer token' {...field} />
                </FormControl>
                <FormDescription>
                  {t('Bearer token if the endpoint requires auth (e.g. gateway admin token).')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='SentinelEmailTo'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Email fallback (optional)')}</FormLabel>
                <FormControl>
                  <Input placeholder='you@example.com' {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Also sends a copy by email. Requires SMTP configured below in System Settings.',
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='SentinelDailyHour'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Daily report hour')}</FormLabel>
                <FormControl>
                  <Input type='number' min={0} max={23} {...field} />
                </FormControl>
                <FormDescription>
                  {t('Hour of day (0-23) to send the daily summary report.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <div className='flex items-center gap-2'>
            <Button type='button' variant='outline' onClick={testPush}>
              {t('Send test notification')}
            </Button>
          </div>

          <Textarea className='hidden' readOnly value='' />

          <SettingsPageFormActions form={form} />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
