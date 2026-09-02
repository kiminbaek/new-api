import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  AlertCircle,
  CheckCircle2,
  RefreshCw,
  Search,
  ShieldCheck,
} from 'lucide-react'
import { useMemo, useState } from 'react'

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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { getModelQuality, type ModelQualityRow, type QualityLevel } from './api'

const LEVEL: Record<QualityLevel, { label: string; className: string }> = {
  stable: {
    label: '稳定',
    className: 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  },
  fluctuating: {
    label: '波动',
    className: 'bg-amber-500/10 text-amber-700 dark:text-amber-400',
  },
  risk: { label: '风险', className: 'bg-destructive/10 text-destructive' },
  insufficient: {
    label: '样本不足',
    className: 'bg-muted text-muted-foreground',
  },
}
const duration = (value: number) =>
  value > 0
    ? value >= 1000
      ? `${(value / 1000).toFixed(1)}s`
      : `${Math.round(value)}ms`
    : '暂无'
const pct = (value: number) =>
  `${Number.isFinite(value) ? value.toFixed(1) : '0.0'}%`

function QualityBadge({ level }: { level: QualityLevel }) {
  const item = LEVEL[level]
  return <Badge className={item.className}>{item.label}</Badge>
}
function StatCard({
  icon: Icon,
  label,
  value,
  detail,
}: {
  icon: typeof Activity
  label: string
  value: string
  detail: string
}) {
  return (
    <Card className='py-4'>
      <CardContent className='space-y-2'>
        <div className='text-muted-foreground flex items-center gap-2 text-sm'>
          <Icon className='size-4' />
          {label}
        </div>
        <div className='text-2xl font-bold tabular-nums'>{value}</div>
        <div className='text-muted-foreground text-xs'>{detail}</div>
      </CardContent>
    </Card>
  )
}
function FailureText({ row }: { row: ModelQualityRow }) {
  if (!row.failure_breakdown_coverage)
    return (
      <span className='text-muted-foreground'>
        未分类历史 {row.unclassified_failures}
      </span>
    )
  return (
    <span className='text-muted-foreground'>
      限流 {row.rate_limited} · 渠道 {row.channel_failures} · 取消{' '}
      {row.client_cancelled} · 其他 {row.other_failures}
    </span>
  )
}

export function ModelQuality() {
  const [hours, setHours] = useState(168)
  const [search, setSearch] = useState('')
  const query = useQuery({
    queryKey: ['model-quality', hours],
    queryFn: () => getModelQuality(hours),
  })
  const rows = useMemo(
    () =>
      query.data?.models.filter((r) =>
        r.model_name.toLowerCase().includes(search.toLowerCase())
      ) ?? [],
    [query.data, search]
  )
  const stable =
    query.data?.models.filter((r) => r.quality_level === 'stable').length ?? 0
  const risk =
    query.data?.models.filter((r) => r.quality_level === 'risk').length ?? 0
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>模型质量</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Select
          value={String(hours)}
          onValueChange={(v) => v && setHours(Number(v))}
        >
          <SelectTrigger className='w-32'>
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
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          <RefreshCw className={query.isFetching ? 'animate-spin' : ''} />
          {query.isFetching ? '刷新中' : '刷新'}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <Alert>
            <ShieldCheck />
            <AlertTitle>真实流量与主动探测分开计分</AlertTitle>
            <AlertDescription>
              调用成功率、延迟和调度健康来自真实请求；回答合理性、模型指纹、无加塞、来源、输出上限和广告注入在主动探测运行前统一显示“未测”，不会用默认满分伪装。
            </AlertDescription>
          </Alert>
          {query.isLoading ? (
            <LoadingState className='min-h-64' />
          ) : query.isError ? (
            <ErrorState
              title='模型质量数据加载失败'
              description={
                query.error instanceof Error
                  ? query.error.message
                  : '请稍后重试'
              }
              onRetry={() => void query.refetch()}
            />
          ) : query.data ? (
            <>
              <div className='grid grid-cols-2 gap-3 xl:grid-cols-4'>
                <StatCard
                  icon={Activity}
                  label='调用'
                  value={query.data.request_count.toLocaleString()}
                  detail={`成功 ${query.data.success_count.toLocaleString()}`}
                />
                <StatCard
                  icon={CheckCircle2}
                  label='真实成功率'
                  value={pct(query.data.success_rate)}
                  detail='每个 request_id 只计一次，分位数取最近 10 万条日志样本'
                />
                <StatCard
                  icon={ShieldCheck}
                  label='稳定模型'
                  value={String(stable)}
                  detail='样本≥20 且成功率≥99%'
                />
                <StatCard
                  icon={AlertCircle}
                  label='风险模型'
                  value={String(risk)}
                  detail='成功率低于 95%'
                />
              </div>
              <Card className='py-4'>
                <CardHeader className='pb-1'>
                  <CardTitle className='text-base'>主动质量探测</CardTitle>
                </CardHeader>
                <CardContent>
                  <div className='grid gap-2 sm:grid-cols-2 xl:grid-cols-4'>
                    {query.data.probe_dimensions.map((d) => (
                      <div
                        key={d.key}
                        className='bg-muted/35 flex items-center justify-between rounded-lg border px-3 py-2 text-sm'
                      >
                        <span>{d.label}</span>
                        <Badge
                          variant={
                            d.status === 'derived' ? 'secondary' : 'outline'
                          }
                        >
                          {d.status === 'derived' ? '流量派生' : '未测'}
                        </Badge>
                      </div>
                    ))}
                  </div>
                </CardContent>
              </Card>
              <Card className='py-4'>
                <CardContent className='space-y-4'>
                  <div className='relative max-w-md'>
                    <Search className='text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2' />
                    <Input
                      aria-label='搜索模型'
                      className='pl-9'
                      placeholder='搜索模型'
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                    />
                  </div>
                  <div className='hidden overflow-x-auto lg:block'>
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>模型</TableHead>
                          <TableHead>质量</TableHead>
                          <TableHead>调用 / 成功</TableHead>
                          <TableHead>成功率</TableHead>
                          <TableHead>P50 / P95</TableHead>
                          <TableHead>首字 P95</TableHead>
                          <TableHead>失败归因</TableHead>
                          <TableHead>调度健康</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {rows.map((r) => (
                          <TableRow key={r.model_name}>
                            <TableCell className='font-medium'>
                              {r.model_name}
                            </TableCell>
                            <TableCell>
                              <QualityBadge level={r.quality_level} />
                            </TableCell>
                            <TableCell className='tabular-nums'>
                              {r.request_count.toLocaleString()} /{' '}
                              {r.success_count.toLocaleString()}
                              <div className='text-muted-foreground text-xs'>
                                额外重试 {r.retry_count.toLocaleString()}
                              </div>
                            </TableCell>
                            <TableCell>
                              <div className='font-semibold tabular-nums'>
                                {pct(r.success_rate)}
                              </div>
                              <div className='text-muted-foreground text-xs'>
                                剔除限流{' '}
                                {pct(r.success_rate_excluding_rate_limit)}
                              </div>
                            </TableCell>
                            <TableCell className='tabular-nums'>
                              {duration(r.p50_latency_ms)} /{' '}
                              {duration(r.p95_latency_ms)}
                            </TableCell>
                            <TableCell>{duration(r.p95_ttft_ms)}</TableCell>
                            <TableCell className='text-xs'>
                              <FailureText row={r} />
                            </TableCell>
                            <TableCell>
                              <div className='font-medium'>
                                {Math.round(r.health_score)} / 100
                              </div>
                              <div className='text-muted-foreground text-xs'>
                                可信 {Math.round(r.confidence * 100)}% ·{' '}
                                {r.route_count} 路由
                                {r.quarantined_routes
                                  ? ` · 隔离 ${r.quarantined_routes}`
                                  : ''}
                              </div>
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                  <div className='grid gap-3 lg:hidden'>
                    {rows.map((r) => (
                      <div key={r.model_name} className='rounded-xl border p-4'>
                        <div className='flex items-start justify-between gap-3'>
                          <div className='min-w-0'>
                            <div className='truncate font-semibold'>
                              {r.model_name}
                            </div>
                            <div className='text-muted-foreground mt-1 text-xs'>
                              {r.request_count.toLocaleString()} 次调用 ·{' '}
                              {r.retry_count.toLocaleString()} 次额外重试
                            </div>
                          </div>
                          <QualityBadge level={r.quality_level} />
                        </div>
                        <div className='mt-4 grid grid-cols-2 gap-3 text-sm'>
                          <div>
                            <div className='text-muted-foreground text-xs'>
                              成功率
                            </div>
                            <div className='font-semibold'>
                              {pct(r.success_rate)}
                            </div>
                          </div>
                          <div>
                            <div className='text-muted-foreground text-xs'>
                              P95 延迟
                            </div>
                            <div className='font-semibold'>
                              {duration(r.p95_latency_ms)}
                            </div>
                          </div>
                          <div>
                            <div className='text-muted-foreground text-xs'>
                              调度健康
                            </div>
                            <div className='font-semibold'>
                              {Math.round(r.health_score)} / 100
                            </div>
                          </div>
                          <div>
                            <div className='text-muted-foreground text-xs'>
                              首字 P95
                            </div>
                            <div className='font-semibold'>
                              {duration(r.p95_ttft_ms)}
                            </div>
                          </div>
                        </div>
                        <div className='mt-3 border-t pt-3 text-xs'>
                          <FailureText row={r} />
                        </div>
                      </div>
                    ))}
                  </div>
                  {rows.length === 0 ? (
                    <div className='text-muted-foreground py-12 text-center text-sm'>
                      没有匹配的模型质量数据
                    </div>
                  ) : null}
                </CardContent>
              </Card>
            </>
          ) : null}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
