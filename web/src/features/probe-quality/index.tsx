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
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  RefreshCw,
  Search,
  ShieldAlert,
  ShieldCheck,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import { sideDrawerContentClassName } from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import {
  getProbeQuality,
  getProbeQualityEvents,
  type ProbeQualityEvent,
  type ProbeQualityRow,
} from './api'

const CATEGORY: Record<string, string> = {
  account_quota: '额度 / 预算池',
  account_disabled: '账户 / 组织停用',
  authentication: '认证失败',
  upstream_capacity: '上游容量不足',
  model_missing: '模型不可用',
  rate_limit: '限流',
  timeout: '超时',
  upstream_5xx: '上游 5xx',
  empty_stream: '空响应',
  network_or_protocol: '网络 / 协议异常',
  request_error: '探测请求不兼容',
  local_error: '本地探测错误',
  recovery_error: '恢复失败',
  unknown: '未分类',
}
const ACTION: Record<string, string> = {
  pass: '通过',
  observe: '观察 / 降权',
  disable_model: '模型隔离',
  disable_channel: '渠道隔离',
  recover: '已恢复',
  ignored_request_error: '不处罚渠道',
}
const pct = (v: number) => `${Number.isFinite(v) ? v.toFixed(1) : '0.0'}%`
const duration = (v: number) =>
  v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${Math.round(v)}ms`
const when = (v: number) => (v ? new Date(v * 1000).toLocaleString() : '暂无')

function toneIconClass(tone: 'default' | 'danger' | 'success') {
  if (tone === 'danger') {
    return 'text-destructive size-4'
  }
  if (tone === 'success') {
    return 'size-4 text-emerald-600'
  }
  return 'size-4'
}

function rangeLabel(hours: number) {
  if (hours === 24) {
    return '24 小时'
  }
  if (hours === 168) {
    return '7 天'
  }
  return '30 天'
}

function StatCard({
  icon: Icon,
  label,
  value,
  detail,
  tone = 'default',
}: {
  icon: typeof Activity
  label: string
  value: string
  detail: string
  tone?: 'default' | 'danger' | 'success'
}) {
  return (
    <Card className='py-4'>
      <CardContent className='space-y-2'>
        <div className='text-muted-foreground flex items-center gap-2 text-sm'>
          <Icon className={toneIconClass(tone)} />
          {label}
        </div>
        <div
          className={
            tone === 'danger'
              ? 'text-destructive text-2xl font-bold tabular-nums'
              : 'text-2xl font-bold tabular-nums'
          }
        >
          {value}
        </div>
        <div className='text-muted-foreground text-xs'>{detail}</div>
      </CardContent>
    </Card>
  )
}

function StatusBadge({ row }: { row: ProbeQualityRow }) {
  if (row.isolated) {
    return <Badge className='bg-destructive/10 text-destructive'>已隔离</Badge>
  }
  if (row.failure_rate >= 50) {
    return <Badge className='bg-destructive/10 text-destructive'>高风险</Badge>
  }
  if (row.failure > 0) {
    return (
      <Badge className='bg-amber-500/10 text-amber-700 dark:text-amber-400'>
        有波动
      </Badge>
    )
  }
  return (
    <Badge className='bg-emerald-500/10 text-emerald-700 dark:text-emerald-400'>
      稳定
    </Badge>
  )
}

export function EventList({ events }: { events: ProbeQualityEvent[] }) {
  if (!events.length) {
    return (
      <div className='text-muted-foreground py-10 text-center text-sm'>
        当前时间范围内没有探测明细
      </div>
    )
  }
  return (
    <div className='space-y-3'>
      {events.map((event) => (
        <div key={event.id} className='rounded-xl border p-3'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <div className='flex items-center gap-2'>
              {event.success ? (
                <CheckCircle2 className='size-4 text-emerald-600' />
              ) : (
                <AlertTriangle className='text-destructive size-4' />
              )}
              <span className='font-medium'>
                {event.success
                  ? '探测通过'
                  : CATEGORY[event.error_category] ||
                    event.error_category ||
                    '探测失败'}
              </span>
            </div>
            <span className='text-muted-foreground text-xs'>
              {when(event.created_at)}
            </span>
          </div>
          <div className='text-muted-foreground mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs'>
            <span>耗时 {duration(event.latency_ms)}</span>
            {event.http_status ? <span>HTTP {event.http_status}</span> : null}
            {event.error_code ? <span>{event.error_code}</span> : null}
            <span>动作：{ACTION[event.action] || event.action || '无'}</span>
          </div>
          {event.reason ? (
            <p className='mt-2 text-sm break-words'>{event.reason}</p>
          ) : null}
          {event.suggestion ? (
            <p className='text-muted-foreground mt-1 text-xs break-words'>
              建议：{event.suggestion}
            </p>
          ) : null}
        </div>
      ))}
    </div>
  )
}

export function ProbeQuality() {
  const [hours, setHours] = useState(168)
  const [search, setSearch] = useState('')
  const [view, setView] = useState<'all' | 'failed' | 'isolated'>('all')
  const [selected, setSelected] = useState<ProbeQualityRow | null>(null)
  const query = useQuery({
    queryKey: ['probe-quality', hours],
    queryFn: () => getProbeQuality(hours),
  })
  const events = useQuery({
    queryKey: [
      'probe-quality-events',
      hours,
      selected?.channel_id,
      selected?.model,
    ],
    queryFn: () => {
      if (!selected) {
        return Promise.resolve([])
      }
      return getProbeQualityEvents(hours, selected.channel_id, selected.model)
    },
    enabled: selected !== null,
  })
  const rows = useMemo(() => {
    const term = search.trim().toLowerCase()
    return (
      query.data?.rows.filter((row) => {
        if (view === 'failed' && row.failure === 0) return false
        if (view === 'isolated' && !row.isolated) return false
        return (
          !term ||
          `${row.channel_name} ${row.channel_id} ${row.model} ${CATEGORY[row.last_error_category] || row.last_error_category}`
            .toLowerCase()
            .includes(term)
        )
      }) ?? []
    )
  }, [query.data, search, view])
  const categories = query.data?.categories.slice(0, 5) ?? []

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>探测质量</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <div className='grid w-full grid-cols-[minmax(0,1fr)_auto] gap-2 sm:flex sm:w-auto'>
          <Select
            value={String(hours)}
            onValueChange={(v) => v && setHours(Number(v))}
          >
            <SelectTrigger className='h-10 w-full sm:h-7 sm:w-32'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='24'>近 24 小时</SelectItem>
              <SelectItem value='168'>近 7 天</SelectItem>
              <SelectItem value='720'>近 30 天</SelectItem>
            </SelectContent>
          </Select>
          <Button
            variant='outline'
            size='sm'
            className='h-10 sm:h-7'
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <RefreshCw className={query.isFetching ? 'animate-spin' : ''} />
            刷新
          </Button>
        </div>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <Alert>
            <ShieldCheck />
            <AlertTitle>定时逐渠道 × 模型探测质量</AlertTitle>
            <AlertDescription>
              这里只统计“健康巡检”结果，不混入用户真实调用。用于回答哪个渠道的哪个模型，在
              1 天、7 天或 30
              天内失败了多少次、为什么失败、故障率如何，以及系统采取了什么动作。
            </AlertDescription>
          </Alert>
          {(() => {
            if (query.isLoading) {
              return <LoadingState className='min-h-64' />
            }
            if (query.isError) {
              return (
                <ErrorState
                  title='探测质量数据加载失败'
                  description={
                    query.error instanceof Error
                      ? query.error.message
                      : '请稍后重试'
                  }
                  onRetry={() => void query.refetch()}
                />
              )
            }
            if (!query.data) {
              return null
            }
            return (
              <>
                <div className='grid grid-cols-2 gap-3 xl:grid-cols-4'>
                  <StatCard
                    icon={Activity}
                    label='探测总数'
                    value={query.data.total.toLocaleString()}
                    detail={`成功 ${query.data.success.toLocaleString()} · 失败 ${query.data.failure.toLocaleString()}`}
                  />
                  <StatCard
                    icon={ShieldAlert}
                    label='探测故障率'
                    value={pct(query.data.failure_rate)}
                    detail={`成功率 ${pct(query.data.success_rate)}`}
                    tone={query.data.failure > 0 ? 'danger' : 'success'}
                  />
                  <StatCard
                    icon={AlertTriangle}
                    label='受影响组合'
                    value={String(query.data.affected_pairs)}
                    detail='至少失败过一次的渠道 × 模型'
                    tone={query.data.affected_pairs ? 'danger' : 'success'}
                  />
                  <StatCard
                    icon={ShieldCheck}
                    label='当前隔离'
                    value={String(query.data.isolated_pairs)}
                    detail='正在等待恢复探测的组合'
                    tone={query.data.isolated_pairs ? 'danger' : 'success'}
                  />
                </div>
                <div className='grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(260px,1fr)]'>
                  <Card className='py-4'>
                    <CardHeader className='pb-1'>
                      <CardTitle className='text-base'>每日探测趋势</CardTitle>
                    </CardHeader>
                    <CardContent>
                      <div className='h-64'>
                        {query.data.trend.length ? (
                          <ResponsiveContainer width='100%' height='100%'>
                            <AreaChart data={query.data.trend}>
                              <CartesianGrid
                                strokeDasharray='3 3'
                                className='opacity-20'
                              />
                              <XAxis
                                dataKey='date'
                                fontSize={12}
                                tickLine={false}
                              />
                              <YAxis
                                allowDecimals={false}
                                fontSize={12}
                                tickLine={false}
                                width={36}
                              />
                              <Tooltip />
                              <Area
                                type='monotone'
                                dataKey='success'
                                name='成功'
                                stackId='probe'
                                stroke='#10b981'
                                fill='#10b981'
                                fillOpacity={0.28}
                              />
                              <Area
                                type='monotone'
                                dataKey='failure'
                                name='失败'
                                stackId='probe'
                                stroke='#ef4444'
                                fill='#ef4444'
                                fillOpacity={0.35}
                              />
                            </AreaChart>
                          </ResponsiveContainer>
                        ) : (
                          <div className='text-muted-foreground flex h-full items-center justify-center text-sm'>
                            尚无探测历史；下一轮巡检完成后开始累计
                          </div>
                        )}
                      </div>
                    </CardContent>
                  </Card>
                  <Card className='py-4'>
                    <CardHeader className='pb-1'>
                      <CardTitle className='text-base'>
                        失败原因 Top 5
                      </CardTitle>
                    </CardHeader>
                    <CardContent className='space-y-3'>
                      {categories.length ? (
                        categories.map((item, index) => (
                          <div
                            key={item.category}
                            className='flex items-center justify-between rounded-lg border px-3 py-2.5'
                          >
                            <div className='flex min-w-0 items-center gap-2'>
                              <span className='text-muted-foreground text-xs tabular-nums'>
                                #{index + 1}
                              </span>
                              <span className='truncate text-sm'>
                                {CATEGORY[item.category] || item.category}
                              </span>
                            </div>
                            <Badge variant='secondary'>{item.count}</Badge>
                          </div>
                        ))
                      ) : (
                        <div className='text-muted-foreground py-12 text-center text-sm'>
                          当前范围没有失败
                        </div>
                      )}
                    </CardContent>
                  </Card>
                </div>
                <Card className='py-4'>
                  <CardContent className='space-y-4'>
                    <div className='flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between'>
                      <div className='relative w-full max-w-lg'>
                        <Search className='text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2' />
                        <Input
                          aria-label='搜索渠道、模型或错误'
                          className='pl-9'
                          placeholder='搜索渠道、模型或错误类别'
                          value={search}
                          onChange={(e) => setSearch(e.target.value)}
                        />
                      </div>
                      <div className='grid grid-cols-3 gap-2'>
                        <Button
                          size='sm'
                          variant={view === 'all' ? 'default' : 'outline'}
                          onClick={() => setView('all')}
                        >
                          全部
                        </Button>
                        <Button
                          size='sm'
                          variant={view === 'failed' ? 'default' : 'outline'}
                          onClick={() => setView('failed')}
                        >
                          仅失败
                        </Button>
                        <Button
                          size='sm'
                          variant={view === 'isolated' ? 'default' : 'outline'}
                          onClick={() => setView('isolated')}
                        >
                          当前隔离
                        </Button>
                      </div>
                    </div>
                    <div className='hidden overflow-x-auto lg:block'>
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>渠道 / 模型</TableHead>
                            <TableHead>状态</TableHead>
                            <TableHead>成功 / 失败</TableHead>
                            <TableHead>故障率</TableHead>
                            <TableHead>平均延迟</TableHead>
                            <TableHead>连续失败</TableHead>
                            <TableHead>最近结果</TableHead>
                            <TableHead className='text-right'>明细</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {rows.map((row) => (
                            <TableRow key={`${row.channel_id}|${row.model}`}>
                              <TableCell>
                                <div className='font-medium'>
                                  {row.channel_name}{' '}
                                  <span className='text-muted-foreground'>
                                    #{row.channel_id}
                                  </span>
                                </div>
                                <div
                                  className='text-muted-foreground max-w-64 truncate text-xs'
                                  title={row.model}
                                >
                                  {row.model}
                                </div>
                              </TableCell>
                              <TableCell>
                                <StatusBadge row={row} />
                              </TableCell>
                              <TableCell className='tabular-nums'>
                                <span className='text-emerald-600'>
                                  {row.success}
                                </span>{' '}
                                /{' '}
                                <span className='text-destructive'>
                                  {row.failure}
                                </span>
                                <div className='text-muted-foreground text-xs'>
                                  共 {row.total}
                                </div>
                              </TableCell>
                              <TableCell
                                className={
                                  row.failure_rate
                                    ? 'text-destructive font-semibold tabular-nums'
                                    : 'font-semibold tabular-nums'
                                }
                              >
                                {pct(row.failure_rate)}
                              </TableCell>
                              <TableCell>
                                {duration(row.avg_latency_ms)}
                              </TableCell>
                              <TableCell className='tabular-nums'>
                                {row.consecutive_failures}
                              </TableCell>
                              <TableCell>
                                <div
                                  className='max-w-56 truncate text-sm'
                                  title={row.last_reason}
                                >
                                  {row.last_success
                                    ? '通过'
                                    : CATEGORY[row.last_error_category] ||
                                      row.last_error_category ||
                                      '失败'}
                                </div>
                                <div className='text-muted-foreground text-xs'>
                                  {when(row.last_probe_at)}
                                </div>
                              </TableCell>
                              <TableCell className='text-right'>
                                <Button
                                  size='sm'
                                  variant='outline'
                                  onClick={() => setSelected(row)}
                                >
                                  查看
                                </Button>
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                    <div className='grid gap-3 lg:hidden'>
                      {rows.map((row) => (
                        <button
                          type='button'
                          key={`${row.channel_id}|${row.model}`}
                          className='rounded-xl border p-4 text-left'
                          onClick={() => setSelected(row)}
                        >
                          <div className='flex items-start justify-between gap-3'>
                            <div className='min-w-0'>
                              <div className='font-semibold'>
                                {row.channel_name}{' '}
                                <span className='text-muted-foreground'>
                                  #{row.channel_id}
                                </span>
                              </div>
                              <div className='text-muted-foreground truncate text-xs'>
                                {row.model}
                              </div>
                            </div>
                            <StatusBadge row={row} />
                          </div>
                          <div className='mt-4 grid grid-cols-2 gap-3 text-sm'>
                            <div>
                              <div className='text-muted-foreground text-xs'>
                                故障率
                              </div>
                              <div
                                className={
                                  row.failure_rate
                                    ? 'text-destructive font-semibold'
                                    : 'font-semibold'
                                }
                              >
                                {pct(row.failure_rate)}
                              </div>
                            </div>
                            <div>
                              <div className='text-muted-foreground text-xs'>
                                成功 / 失败
                              </div>
                              <div className='font-semibold'>
                                {row.success} / {row.failure}
                              </div>
                            </div>
                            <div>
                              <div className='text-muted-foreground text-xs'>
                                平均延迟
                              </div>
                              <div>{duration(row.avg_latency_ms)}</div>
                            </div>
                            <div>
                              <div className='text-muted-foreground text-xs'>
                                最后探测
                              </div>
                              <div>{when(row.last_probe_at)}</div>
                            </div>
                          </div>
                          {!row.last_success && row.last_reason ? (
                            <div className='text-muted-foreground mt-3 line-clamp-2 border-t pt-3 text-xs'>
                              {row.last_reason}
                            </div>
                          ) : null}
                        </button>
                      ))}
                    </div>
                    {!rows.length ? (
                      <div className='text-muted-foreground py-12 text-center text-sm'>
                        {query.data.total
                          ? '没有匹配的探测组合'
                          : '尚无历史数据；下一轮逐模型巡检完成后自动显示'}
                      </div>
                    ) : null}
                  </CardContent>
                </Card>
              </>
            )
          })()}
        </div>
      </SectionPageLayout.Content>
      <Sheet
        open={selected !== null}
        onOpenChange={(open) => !open && setSelected(null)}
      >
        <SheetContent
          className={sideDrawerContentClassName('max-w-none sm:!max-w-2xl')}
        >
          <SheetHeader className='border-b px-5 py-4'>
            <SheetTitle>
              {selected
                ? `${selected.channel_name} #${selected.channel_id}`
                : '探测明细'}
            </SheetTitle>
            <SheetDescription>
              {selected?.model} · 近 {rangeLabel(hours)} · 最近 100 条
            </SheetDescription>
          </SheetHeader>
          <div className='min-h-0 flex-1 overflow-y-auto px-5 py-4'>
            {(() => {
              if (events.isLoading) {
                return <LoadingState className='min-h-48' />
              }
              if (events.isError) {
                return (
                  <ErrorState
                    title='探测明细加载失败'
                    description={
                      events.error instanceof Error
                        ? events.error.message
                        : '请稍后重试'
                    }
                    onRetry={() => void events.refetch()}
                  />
                )
              }
              return <EventList events={events.data ?? []} />
            })()}
          </div>
        </SheetContent>
      </Sheet>
    </SectionPageLayout>
  )
}
