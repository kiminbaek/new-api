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
// [CUSTOM] 公开平台概览：实时指标 + 7 日趋势 + 模型实时成功率
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Activity,
  BarChart3,
  LineChart,
  TrendingUp,
  Users,
  UsersRound,
} from 'lucide-react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import { PublicLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

interface ModelRate {
  model: string
  success_rate: number
  succ: number
  samples: number
}

interface OverviewData {
  total_users: number
  total_requests: number
  today_requests: number
  active_users_30d: number
  success_rate: number
  succ: number
  samples: number
  trend: { date: string; count: number }[]
  model_rates: ModelRate[]
}

function fmt(n: number): string {
  return n.toLocaleString('en-US')
}

function rateColor(rate: number): string {
  if (rate >= 95) return 'bg-emerald-500'
  if (rate >= 80) return 'bg-amber-500'
  return 'bg-red-500'
}

export function PublicOverview() {
  const { t } = useTranslation()
  const [data, setData] = useState<OverviewData | null>(null)
  const [error, setError] = useState<string>('')

  const load = useCallback(async () => {
    try {
      const res = await fetch('/api/public/overview')
      const json = await res.json()
      if (json.success) {
        setData(json.data)
        setError('')
      } else {
        setError(json.message || t('加载失败'))
      }
    } catch (e) {
      setError(t('网络错误，正在重试…'))
    }
  }, [t])

  useEffect(() => {
    load()
    const timer = setInterval(load, 30000) // 30s 自动刷新
    return () => clearInterval(timer)
  }, [load])

  const cards = [
    {
      label: t('总用户量'),
      value: data ? fmt(data.total_users) : '',
      desc: t('平台注册用户'),
      icon: <Users className='h-4 w-4' />,
    },
    {
      label: t('累计调用'),
      value: data ? fmt(data.total_requests) : '',
      desc: t('平台总调用量'),
      icon: <TrendingUp className='h-4 w-4' />,
    },
    {
      label: t('今日调用'),
      value: data ? fmt(data.today_requests) : '',
      desc: t('今日实时请求'),
      icon: <BarChart3 className='h-4 w-4' />,
    },
    {
      label: t('活跃用户'),
      value: data ? fmt(data.active_users_30d) : '',
      desc: t('近 30 天调用用户'),
      icon: <UsersRound className='h-4 w-4' />,
    },
    {
      label: t('成功率'),
      value: data ? `${data.success_rate.toFixed(1)}%` : '',
      desc: t('实时滚动窗口统计'),
      icon: <Activity className='h-4 w-4' />,
    },
  ]

  return (
    <PublicLayout>
      <div className='mx-auto w-full max-w-6xl px-4 py-8'>
        <div className='mb-6 flex items-center gap-3'>
          <div className='flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary'>
            <LineChart className='h-5 w-5' />
          </div>
          <div>
            <h1 className='text-xl font-semibold'>{t('实时平台概览')}</h1>
            <p className='text-sm text-muted-foreground'>
              {t('平台核心指标、近 7 日调用趋势与模型实时成功率')}
            </p>
          </div>
          <Badge variant='secondary' className='ml-auto'>
            {t('实时数据')} · 30s
          </Badge>
        </div>

        {error && (
          <div className='mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive'>
            {error}
          </div>
        )}

        <div className='grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5'>
          {cards.map((c) => (
            <Card key={c.label}>
              <CardContent className='p-4'>
                <div className='flex items-start justify-between'>
                  <span className='text-xs text-muted-foreground'>
                    {c.label}
                  </span>
                  <span className='text-muted-foreground/70'>{c.icon}</span>
                </div>
                <div className='mt-2 text-2xl font-semibold tabular-nums'>
                  {data ? c.value : <Skeleton className='h-8 w-20' />}
                </div>
                <div className='mt-1 text-xs text-muted-foreground'>
                  {c.desc}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>

        <Card className='mt-4'>
          <CardContent className='p-4'>
            <h2 className='mb-3 text-sm font-medium'>{t('近 7 日调用量')}</h2>
            <div className='h-64'>
              {data ? (
                <ResponsiveContainer width='100%' height='100%'>
                  <AreaChart data={data.trend}>
                    <defs>
                      <linearGradient id='pv' x1='0' y1='0' x2='0' y2='1'>
                        <stop
                          offset='5%'
                          stopColor='var(--primary)'
                          stopOpacity={0.35}
                        />
                        <stop
                          offset='95%'
                          stopColor='var(--primary)'
                          stopOpacity={0}
                        />
                      </linearGradient>
                    </defs>
                    <CartesianGrid strokeDasharray='3 3' className='opacity-20' />
                    <XAxis dataKey='date' fontSize={12} tickLine={false} />
                    <YAxis fontSize={12} tickLine={false} width={48} />
                    <Tooltip />
                    <Area
                      type='monotone'
                      dataKey='count'
                      stroke='var(--primary)'
                      fill='url(#pv)'
                      strokeWidth={2}
                    />
                  </AreaChart>
                </ResponsiveContainer>
              ) : (
                <Skeleton className='h-full w-full' />
              )}
            </div>
          </CardContent>
        </Card>

        <Card className='mt-4'>
          <CardContent className='p-4'>
            <div className='mb-3 flex items-center justify-between'>
              <h2 className='text-sm font-medium'>{t('模型实时成功率')}</h2>
              <span className='text-xs text-muted-foreground'>
                {t('按滚动窗口统计，每次请求实时更新')}
              </span>
            </div>
            {data && data.model_rates.length > 0 ? (
              <div className='overflow-x-auto'>
                <table className='w-full text-sm'>
                  <thead>
                    <tr className='border-b text-left text-xs text-muted-foreground'>
                      <th className='py-2 pr-4'>{t('模型')}</th>
                      <th className='py-2 pr-4 w-[38%]'>{t('成功率')}</th>
                      <th className='py-2 pr-4 text-right'>{t('成功')}</th>
                      <th className='py-2 text-right'>{t('样本数')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.model_rates.map((m) => (
                      <tr
                        key={m.model}
                        className='border-b last:border-0'
                      >
                        <td className='py-2 pr-4 font-medium'>{m.model}</td>
                        <td className='py-2 pr-4'>
                          <div className='flex items-center gap-2'>
                            <div className='h-2 flex-1 overflow-hidden rounded-full bg-muted'>
                              <div
                                className={`h-full rounded-full ${rateColor(m.success_rate)}`}
                                style={{ width: `${Math.max(m.success_rate, 2)}%` }}
                              />
                            </div>
                            <span className='w-14 text-right tabular-nums'>
                              {m.samples > 0
                                ? `${m.success_rate.toFixed(1)}%`
                                : '-'}
                            </span>
                          </div>
                        </td>
                        <td className='py-2 pr-4 text-right tabular-nums'>
                          {fmt(m.succ)}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {fmt(m.samples)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : data ? (
              <div className='py-8 text-center text-sm text-muted-foreground'>
                {t('暂无调用数据，发起请求后此处将展示各模型实时成功率')}
              </div>
            ) : (
              <Skeleton className='h-32 w-full' />
            )}
          </CardContent>
        </Card>
      </div>
    </PublicLayout>
  )
}
