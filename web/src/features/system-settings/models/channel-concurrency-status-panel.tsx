import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { api } from '@/lib/api'

type ConcurrencyItem = {
  key: string
  current: number
  peak: number
  rejected: number
}

type ConcurrencyStatus = {
  scope: 'local' | 'redis'
  warning?: string
  items: ConcurrencyItem[]
}

const REFRESH_INTERVAL_MS = 5000

function dimensionLabel(key: string): string {
  if (key.includes(':model:')) return '模型'
  if (key.includes(':key:')) return '上游 Key'
  return '渠道'
}

function displayKey(key: string): string {
  return key.replace(/^channel:/, '#').replace(':model:', ' · ').replace(':key:', ' · Key ')
}

export function ChannelConcurrencyStatusPanel() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<ConcurrencyStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)

  const load = useCallback(async () => {
    try {
      const response = await api.get('/api/channel/concurrency/status')
      if (response.data?.success) {
        setStatus(response.data.data as ConcurrencyStatus)
        setError(false)
      } else {
        setError(true)
      }
    } catch {
      setError(true)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = setInterval(() => void load(), REFRESH_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [load])

  const items = status?.items ?? []
  const active = items.filter((item) => item.current > 0).length
  const rejected = items.reduce((sum, item) => sum + item.rejected, 0)

  return (
    <div className='flex min-w-0 flex-col gap-4'>
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div>
          <h4 className='text-sm font-medium'>{t('Upstream concurrency status')}</h4>
          <p className='text-muted-foreground text-sm'>
            {t('Live in-flight usage, peaks, and local capacity rejections. Upstream keys are displayed only as irreversible fingerprints.')}
          </p>
        </div>
        <Badge variant={status?.scope === 'redis' ? 'default' : 'secondary'}>
          {status?.scope === 'redis' ? t('Shared Redis') : t('This instance')}
        </Badge>
      </div>

      {status?.warning ? <p className='text-amber-600 text-sm'>{status.warning}</p> : null}
      {error ? <p className='text-destructive text-sm'>{t('Failed to load concurrency status. The last successful snapshot is retained.')}</p> : null}

      <div className='grid grid-cols-2 gap-3 lg:grid-cols-3'>
        <div className='rounded-xl border p-3'><div className='text-muted-foreground text-xs'>{t('Tracked capacity dimensions')}</div><div className='mt-1 text-2xl font-semibold'>{items.length}</div></div>
        <div className='rounded-xl border p-3'><div className='text-muted-foreground text-xs'>{t('Currently active')}</div><div className='mt-1 text-2xl font-semibold'>{active}</div></div>
        <div className='rounded-xl border p-3'><div className='text-muted-foreground text-xs'>{t('Capacity rejections')}</div><div className='mt-1 text-2xl font-semibold'>{rejected}</div></div>
      </div>

      {!loading && items.length === 0 ? <p className='text-muted-foreground text-sm'>{t('No concurrency limits have been exercised on this instance yet.')}</p> : null}
      {loading && items.length === 0 ? <p className='text-muted-foreground text-sm'>{t('Loading...')}</p> : null}
      {items.length > 0 ? (
        <div className='min-w-0 overflow-x-auto'>
          <Table>
            <TableHeader><TableRow><TableHead>{t('Dimension')}</TableHead><TableHead>{t('Scope')}</TableHead><TableHead>{t('In flight')}</TableHead><TableHead>{t('Peak')}</TableHead><TableHead>{t('Rejected')}</TableHead></TableRow></TableHeader>
            <TableBody>{items.map((item) => <TableRow key={item.key}><TableCell><Badge variant='outline'>{dimensionLabel(item.key)}</Badge></TableCell><TableCell className='font-mono text-xs'>{displayKey(item.key)}</TableCell><TableCell>{item.current}</TableCell><TableCell>{item.peak}</TableCell><TableCell>{item.rejected}</TableCell></TableRow>)}</TableBody>
          </Table>
        </div>
      ) : null}
    </div>
  )
}
