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
import { useQueryClient } from '@tanstack/react-query'
import {
  BellRing,
  CheckCircle2,
  Loader2,
  Radio,
  ShieldCheck,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
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

import {
  SettingsForm,
  SettingsControlGroup,
  SettingsFormGrid,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { updateSystemOptionsBulk } from '../api'
import { safeNumberFieldProps } from '../utils/numeric-field'

const sentinelFormSchema = z.object({
  SentinelEnabled: z.boolean(),
  SentinelWebhookURL: z.string(),
  SentinelWebhookAuth: z.string(),
  SentinelEmailTo: z.string(),
  SentinelDailyHour: z.coerce.number().int().min(0).max(23),
})

type SentinelFormInput = z.input<typeof sentinelFormSchema>
type SentinelFormValues = z.output<typeof sentinelFormSchema>

interface SentinelSectionProps {
  defaultValues: {
    SentinelEnabled: boolean
    SentinelWebhookURL: string
    SentinelWebhookAuth: string
    SentinelEmailTo: string
    SentinelDailyHour: number
  }
}

export function SentinelSection({ defaultValues }: SentinelSectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [isSaving, setIsSaving] = useState(false)
  const baselineRef = useRef<Partial<SentinelFormValues>>({})
  const [isTesting, setIsTesting] = useState(false)
  const [testResults, setTestResults] = useState<Record<
    string,
    { success: boolean; error?: string }
  > | null>(null)

  const form = useForm<SentinelFormInput, unknown, SentinelFormValues>({
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
      SentinelEnabled: defaultValues.SentinelEnabled ?? false,
      SentinelWebhookURL: defaultValues.SentinelWebhookURL ?? '',
      SentinelWebhookAuth: '', // Secret is intentionally never returned by the API.
      SentinelEmailTo: defaultValues.SentinelEmailTo ?? '',
      SentinelDailyHour: defaultValues.SentinelDailyHour ?? 8,
    }
    form.reset(values)
    baselineRef.current = values
  }, [defaultValues, form])

  const onSubmit = async (values: SentinelFormValues) => {
    const updates = (
      Object.keys(values) as Array<keyof SentinelFormValues>
    ).filter((key) => {
      if (key === 'SentinelWebhookAuth' && values[key] === '') return false
      return String(values[key]) !== String(baselineRef.current[key])
    })

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    setIsSaving(true)
    try {
      await updateSystemOptionsBulk({
        values: Object.fromEntries(updates.map((key) => [key, String(values[key])])),
      })
      baselineRef.current = values
      await queryClient.invalidateQueries({ queryKey: ['system-options'] })
      toast.success(t('Settings saved'))
    } finally {
      setIsSaving(false)
    }
  }

  const testPush = async () => {
    setIsTesting(true)
    setTestResults(null)
    try {
      const res = await fetch('/api/sentinel/test', { method: 'POST' })
      const data = await res.json()
      setTestResults(data.channels ?? null)
      if (data.success) {
        toast.success(t('Test notification sent — check your channel'))
      } else {
        toast.error(data.message || t('Test failed'))
      }
    } catch {
      toast.error(t('Request failed'))
    } finally {
      setIsTesting(false)
    }
  }

  return (
    <SettingsSection title={t('Sentinel Push')}>
      <div className='mb-5 grid gap-3 md:grid-cols-3'>
        <Card size='sm'>
          <CardContent className='flex items-center gap-3'>
            <div className='bg-primary/10 text-primary rounded-lg p-2'>
              <BellRing className='size-5' />
            </div>
            <div>
              <div className='font-medium'>{t('Proactive alerts')}</div>
              <div className='text-muted-foreground text-xs'>
                {t('Disable, recovery and redundancy events')}
              </div>
            </div>
          </CardContent>
        </Card>
        <Card size='sm'>
          <CardContent className='flex items-center gap-3'>
            <div className='rounded-lg bg-emerald-500/10 p-2 text-emerald-600'>
              <ShieldCheck className='size-5' />
            </div>
            <div>
              <div className='font-medium'>{t('Non-blocking delivery')}</div>
              <div className='text-muted-foreground text-xs'>
                {t('Notification failures never affect relay traffic')}
              </div>
            </div>
          </CardContent>
        </Card>
        <Card size='sm'>
          <CardContent className='flex items-center gap-3'>
            <div className='rounded-lg bg-amber-500/10 p-2 text-amber-600'>
              <Radio className='size-5' />
            </div>
            <div>
              <div className='font-medium'>{t('24-hour deduplication')}</div>
              <div className='text-muted-foreground text-xs'>
                {t('Deduplicated by event, channel and model')}
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
      <Alert className='mb-5'>
        <ShieldCheck />
        <AlertTitle>
          {form.watch('SentinelEnabled')
            ? t('Sentinel is enabled')
            : t('Sentinel is disabled')}
        </AlertTitle>
        <AlertDescription>
          {t(
            'Configure a webhook, email, or both. The saved token is never returned to the browser.'
          )}
        </AlertDescription>
      </Alert>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={isSaving}
          />

          <SettingsControlGroup>
            <FormField
              control={form.control}
              name='SentinelEnabled'
              render={({ field }) => (
                <SettingsSwitchField
                  checked={field.value}
                  onCheckedChange={field.onChange}
                  label={t('Enable sentinel push')}
                  description={t(
                    'Off or unconfigured channels = fully silent, never blocks the main flow'
                  )}
                />
              )}
            />
          </SettingsControlGroup>

          <SettingsFormGrid>
            <FormField
              control={form.control}
              name='SentinelWebhookURL'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Webhook URL')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder='http://127.0.0.1:3018/api/webui/send'
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Generic JSON webhook. Fill in the QQ Gateway send endpoint to receive pushes in QQ.'
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
                    <Input
                      type='password'
                      placeholder='Bearer token'
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Bearer token if the endpoint requires auth (e.g. gateway admin token).'
                    )}
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
                      'Also sends a copy by email. Requires SMTP configured below in System Settings.'
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
                    <Input
                      type='number'
                      min={0}
                      max={23}
                      {...safeNumberFieldProps(field)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Hour of day (0-23) to send the daily summary report.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </SettingsFormGrid>

          <div className='flex items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              disabled={isTesting}
              onClick={testPush}
            >
              {isTesting ? (
                <Loader2 className='animate-spin' data-icon='inline-start' />
              ) : (
                <Radio data-icon='inline-start' />
              )}
              {isTesting
                ? t('Testing channels...')
                : t('Send test notification')}
            </Button>
            <span className='text-muted-foreground text-xs'>
              {t(
                'The saved token is hidden. Leave it blank to keep the current token.'
              )}
            </span>
          </div>

          {testResults ? (
            <div className='grid gap-2 sm:grid-cols-2'>
              {Object.entries(testResults).map(([channel, result]) => (
                <Alert
                  key={channel}
                  variant={result.success ? 'default' : 'destructive'}
                >
                  {result.success ? <CheckCircle2 /> : <Radio />}
                  <AlertTitle className='capitalize'>{channel}</AlertTitle>
                  <AlertDescription>
                    {result.success
                      ? t('Delivery succeeded')
                      : result.error || t('Delivery failed')}
                  </AlertDescription>
                </Alert>
              ))}
            </div>
          ) : null}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
