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
// [CUSTOM] 智能自动禁用只读看板：模型级下线是内存态，界面上必须能看见
// 「谁被下线了、为什么、下次什么时候探测」，否则调度就是黑盒。
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { api } from '@/lib/api'

type SmartDownItem = {
  channel_id: number
  channel_name: string
  model: string
  level: 'model' | 'channel'
  reason: string
  disabled_at: number
  next_probe_at: number
  attempts: number
  last_error?: string
  probing: boolean
  recent_samples?: number
  recent_succ?: number
  health_score: number
  confidence: number
  attribution: {
    category: string
    confidence: number
    action: string
    summary: string
  }
  canary_stage: number
  canary_percent: number
  canary_success: number
  canary_failure: number
}

function formatRate(item: SmartDownItem): string {
  const samples = item.recent_samples ?? 0
  const succ = item.recent_succ ?? 0
  if (samples <= 0) return '—'
  const pct = Math.round((succ / samples) * 100)
  return `${pct}% (${succ}/${samples})`
}

type SmartDisableStatus = {
  enabled: boolean
  items: SmartDownItem[]
  probe_budget: number
  disable_score: number
  recovery_score: number
  decay_half_life_hours: number
}

const REFRESH_INTERVAL_MS = 15000

function recoveryStatus(
  item: SmartDownItem,
  now: number,
  t: (key: string) => string
): string {
  if (item.canary_stage > 0) return t('Real traffic verification')
  if (item.probing) return t('Probing...')
  return formatCountdown(item.next_probe_at, now)
}

function formatCountdown(target: number, now: number): string {
  const diff = target - now
  if (diff <= 0) return '—'
  const mins = Math.floor(diff / 60)
  const secs = diff % 60
  if (mins > 0) return `${mins}m ${secs}s`
  return `${secs}s`
}

export function SmartDisableStatusPanel() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<SmartDisableStatus | null>(null)
  const [loading, setLoading] = useState(false)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.get('/api/channel/smart_disable/status')
      const payload = res?.data
      if (payload?.success) setStatus(payload.data as SmartDisableStatus)
    } catch {
      // 看板失败不打扰用户：静默保留上一次快照
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = setInterval(() => {
      setNow(Math.floor(Date.now() / 1000))
      void load()
    }, REFRESH_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [load])

  const clearOne = async (item: SmartDownItem) => {
    try {
      const res = await api.post('/api/channel/smart_disable/clear', {
        channel_id: item.channel_id,
        model: item.model,
      })
      if (res?.data?.success) {
        toast.success(t('Restored'))
        void load()
      } else {
        toast.error(res?.data?.message || t('Operation failed'))
      }
    } catch {
      toast.error(t('Operation failed'))
    }
  }

  const items = status?.items ?? []

  return (
    <div className='flex min-w-0 flex-col gap-4'>
      <div className='flex flex-col gap-1'>
        <h4 className='text-sm font-medium'>
          {t('Smart auto-disable status')}
        </h4>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Live view of channel-model pairs currently taken offline. Recovery is automatic once a probe request succeeds; manual restore is only a shortcut.'
          )}
        </p>
      </div>

      {status ? (
        <div className='grid grid-cols-2 gap-3 lg:grid-cols-4'>
          <div className='rounded-xl border p-3'>
            <div className='text-muted-foreground text-xs'>
              {t('Active incidents')}
            </div>
            <div className='mt-1 text-2xl font-semibold'>{items.length}</div>
          </div>
          <div className='rounded-xl border p-3'>
            <div className='text-muted-foreground text-xs'>
              {t('Adaptive probe budget')}
            </div>
            <div className='mt-1 text-2xl font-semibold'>
              {status.probe_budget}
            </div>
          </div>
          <div className='rounded-xl border p-3'>
            <div className='text-muted-foreground text-xs'>
              {t('Hysteresis thresholds')}
            </div>
            <div className='mt-1 font-semibold'>
              {status.disable_score} → {status.recovery_score}
            </div>
          </div>
          <div className='rounded-xl border p-3'>
            <div className='text-muted-foreground text-xs'>
              {t('Decay half-life')}
            </div>
            <div className='mt-1 font-semibold'>
              {status.decay_half_life_hours}h
            </div>
          </div>
        </div>
      ) : null}

      {status && !status.enabled ? (
        <p className='text-muted-foreground text-sm'>
          {t('Smart auto-disable is currently off.')}
        </p>
      ) : null}
      {(!status || status.enabled) && items.length === 0 ? (
        <p className='text-muted-foreground text-sm'>
          {loading
            ? t('Loading...')
            : t('All channels and models are healthy.')}
        </p>
      ) : null}
      {(!status || status.enabled) && items.length > 0 ? (
        <div className='min-w-0 overflow-x-auto'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Channel')}</TableHead>
                <TableHead>{t('Model')}</TableHead>
                <TableHead>{t('Scope')}</TableHead>
                <TableHead>{t('Reason')}</TableHead>
                <TableHead>{t('Health / confidence')}</TableHead>
                <TableHead>{t('Attribution')}</TableHead>
                <TableHead>{t('Recovery stage')}</TableHead>
                <TableHead>{t('Next probe')}</TableHead>
                <TableHead>{t('Probes')}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={`${item.channel_id}|${item.model}`}>
                  <TableCell>
                    {item.channel_name || `#${item.channel_id}`}
                    <span className='text-muted-foreground'>
                      {' '}
                      (#{item.channel_id})
                    </span>
                  </TableCell>
                  <TableCell>{item.model || '—'}</TableCell>
                  <TableCell>
                    <Badge
                      variant={
                        item.level === 'channel' ? 'destructive' : 'secondary'
                      }
                    >
                      {item.level === 'channel'
                        ? t('Whole channel')
                        : t('Single model')}
                    </Badge>
                  </TableCell>
                  <TableCell className='max-w-[20rem] truncate'>
                    {item.reason}
                  </TableCell>
                  <TableCell>
                    <div className='font-medium'>
                      {Math.round(item.health_score)} / 100
                    </div>
                    <div className='text-muted-foreground text-xs'>
                      {Math.round(item.confidence * 100)}% · {formatRate(item)}
                    </div>
                  </TableCell>
                  <TableCell className='max-w-[14rem]'>
                    <Badge variant='outline'>
                      {item.attribution?.category || 'unknown'}
                    </Badge>
                    <div className='text-muted-foreground mt-1 truncate text-xs'>
                      {item.attribution?.summary || item.reason}
                    </div>
                  </TableCell>
                  <TableCell>
                    {item.canary_stage > 0 ? (
                      <Badge variant='secondary'>
                        Canary {item.canary_percent}%
                      </Badge>
                    ) : (
                      <Badge variant='destructive'>{t('Quarantined')}</Badge>
                    )}
                  </TableCell>
                  <TableCell>{recoveryStatus(item, now, t)}</TableCell>
                  <TableCell>{item.attempts}</TableCell>
                  <TableCell>
                    <Button
                      variant='outline'
                      size='sm'
                      onClick={() => void clearOne(item)}
                    >
                      {t('Restore now')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : null}
    </div>
  )
}
