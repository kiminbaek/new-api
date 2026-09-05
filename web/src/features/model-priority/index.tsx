import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  AlertCircle,
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Info,
  Minus,
  RefreshCw,
  Settings2,
  Search,
  SlidersHorizontal,
} from 'lucide-react'
import { useMemo, useState } from 'react'

import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
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

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getModelPriority, type ModelPriorityRow } from './api'
import { QuarantineDetailsDialog, RoutingStatusBadge } from './routing-status'

type StatFilter = 'all' | 'neg' | 'pos' | 'adjusted' | 'quarantined' | 'canary'

function deltaTone(delta: number) {
  if (delta < 0) return 'text-destructive'
  if (delta > 0) return 'text-emerald-600 dark:text-emerald-400'
  return 'text-muted-foreground'
}

function DeltaValue({ delta }: { delta: number }) {
  let icon = <Minus className='size-3.5' />
  if (delta < 0) icon = <ArrowDown className='size-3.5' />
  if (delta > 0) icon = <ArrowUp className='size-3.5' />
  return (
    <span
      className={`inline-flex items-center gap-1 font-semibold ${deltaTone(delta)}`}
    >
      {icon}
      {delta > 0 ? '+' : ''}
      {delta}
    </span>
  )
}

export function ModelPriority() {
  const user = useAuthStore((state) => state.auth.user)
  const canConfigureGroups = Boolean(user && user.role >= ROLE.SUPER_ADMIN)
  const {
    data: rows = [],
    isLoading,
    isFetching,
    isError,
    error,
    dataUpdatedAt,
    refetch,
  } = useQuery({
    queryKey: ['model-priority'],
    queryFn: getModelPriority,
  })

  const [fModel, setFModel] = useState('')
  const [fChannel, setFChannel] = useState('')
  const [fDelta, setFDelta] = useState('all')
  const [fEnabled, setFEnabled] = useState('all')
  const [fRouting, setFRouting] = useState('all')
  const [sortBy, setSortBy] = useState<keyof ModelPriorityRow>('model')
  const [sortDir, setSortDir] = useState<1 | -1>(1)

  const stats = useMemo(
    () => ({
      total: rows.length,
      neg: rows.filter((row) => row.delta < 0).length,
      pos: rows.filter((row) => row.delta > 0).length,
      adjusted: rows.filter((row) => row.delta !== 0).length,
      quarantined: rows.filter((row) => row.routing_status === 'quarantined').length,
      canary: rows.filter((row) => row.routing_status === 'canary').length,
    }),
    [rows]
  )

  const filtered = useMemo(() => {
    const filteredRows = rows.filter((row) => {
      if (fModel && !row.model.toLowerCase().includes(fModel.toLowerCase())) {
        return false
      }
      if (
        fChannel &&
        !row.channel_name.toLowerCase().includes(fChannel.toLowerCase())
      ) {
        return false
      }
      if (fDelta === 'neg' && row.delta >= 0) return false
      if (fDelta === 'pos' && row.delta <= 0) return false
      if (fDelta === 'zero' && row.delta !== 0) return false
      if (fDelta === 'adjusted' && row.delta === 0) return false
      if (fEnabled === 'on' && !row.enabled) return false
      if (fEnabled === 'off' && row.enabled) return false
      if (fRouting !== 'all' && row.routing_status !== fRouting) return false
      return true
    })
    return [...filteredRows].sort((a, b) => {
      const aValue = a[sortBy]
      const bValue = b[sortBy]
      if (typeof aValue === 'string') {
        return aValue.localeCompare(bValue as string) * sortDir
      }
      return ((aValue as number) - (bValue as number)) * sortDir
    })
  }, [rows, fModel, fChannel, fDelta, fEnabled, fRouting, sortBy, sortDir])

  const sortColumn = (key: keyof ModelPriorityRow) => {
    if (sortBy === key) {
      setSortDir((current) => (current === 1 ? -1 : 1))
      return
    }
    setSortBy(key)
    setSortDir(1)
  }

  const applyStatFilter = (filter: StatFilter) => {
    if (filter === 'quarantined' || filter === 'canary') {
      setFDelta('all')
      setFRouting(filter)
      return
    }
    setFDelta(filter)
    setFRouting('all')
  }

  const statCards: Array<{
    key: StatFilter
    value: number
    label: string
    detail: string
    color: string
  }> = [
    {
      key: 'all',
      value: stats.total,
      label: '全部路由',
      detail: '渠道 × 模型',
      color: 'text-blue-600',
    },
    {
      key: 'neg',
      value: stats.neg,
      label: '降权中',
      detail: '自动避开不稳定线路',
      color: 'text-destructive',
    },
    {
      key: 'pos',
      value: stats.pos,
      label: '升权中',
      detail: '近期表现优于基准',
      color: 'text-emerald-600',
    },
    {
      key: 'adjusted',
      value: stats.adjusted,
      label: '已调整',
      detail: '算法正在动态干预',
      color: 'text-amber-600',
    },
    {
      key: 'quarantined', value: stats.quarantined, label: '隔离中',
      detail: '已停止该渠道模型路由', color: 'text-destructive',
    },
    {
      key: 'canary', value: stats.canary, label: 'Canary 恢复',
      detail: '少量真实流量验证中', color: 'text-violet-600',
    },
  ]

  const renderSortHead = (label: string, key: keyof ModelPriorityRow) => {
    const active = sortBy === key
    let ariaSort: 'ascending' | 'descending' | 'none' = 'none'
    if (active) {
      ariaSort = sortDir === 1 ? 'ascending' : 'descending'
    }
    return (
      <TableHead aria-sort={ariaSort}>
        <button
          type='button'
          className='hover:text-foreground inline-flex items-center gap-1 font-medium'
          onClick={() => sortColumn(key)}
        >
          {label}
          <ArrowUpDown
            className={`size-3.5 ${active ? 'text-primary' : 'text-muted-foreground'}`}
          />
        </button>
      </TableHead>
    )
  }

  const updatedText = dataUpdatedAt
    ? new Date(dataUpdatedAt).toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      })
    : '尚未更新'

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>模型分级</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        {canConfigureGroups ? (
          <Button
            variant='outline'
            size='sm'
            render={
              <Link
                to='/system-settings/models/$section'
                params={{ section: 'model-groups' }}
              />
            }
          >
            <Settings2 />
            模型组配置
          </Button>
        ) : null}
        <Button
          variant='outline'
          size='sm'
          disabled={isFetching}
          onClick={() => void refetch()}
        >
          <RefreshCw
            className={`mr-2 size-4 ${isFetching ? 'animate-spin' : ''}`}
          />
          {isFetching ? '刷新中' : '刷新'}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <div className='flex flex-col gap-1'>
            <p className='text-muted-foreground text-sm'>
              查看每个渠道 × 模型的基准优先级、实时偏移与当前路由状态。
            </p>
            <div className='flex flex-wrap items-center gap-2 text-xs'>
              <span className='text-muted-foreground'>数据更新时间：{updatedText}</span>
              {!canConfigureGroups ? (
                <Badge variant='outline'>模型组配置需超级管理员</Badge>
              ) : null}
            </div>
          </div>

          <Alert>
            <Info />
            <AlertTitle>如何理解模型分级</AlertTitle>
            <AlertDescription>
              基准值来自渠道或模型配置；有效值是当前真正参与选路的优先级；
              偏移为正表示升权，为负表示降权。隔离与 Canary 状态由健康探测和
              智能禁用策略驱动，本页只展示运行结果，不直接修改路由。
            </AlertDescription>
          </Alert>

          <div className='grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6'>
            {statCards.map((stat) => (
              <button
                key={stat.key}
                type='button'
                aria-pressed={stat.key === 'quarantined' || stat.key === 'canary' ? fRouting === stat.key : fRouting === 'all' && fDelta === stat.key}
                className='focus-visible:ring-ring rounded-xl text-left focus-visible:ring-2 focus-visible:outline-none'
                onClick={() => applyStatFilter(stat.key)}
              >
                <Card
                  className={`h-full py-3 transition-colors ${
                    (stat.key === 'quarantined' || stat.key === 'canary' ? fRouting === stat.key : fRouting === 'all' && fDelta === stat.key)
                      ? 'border-primary bg-primary/5 ring-primary/20 ring-2'
                      : 'hover:bg-muted/30'
                  }`}
                >
                  <CardContent className='space-y-1'>
                    <div
                      className={`text-2xl font-bold tabular-nums ${stat.color}`}
                    >
                      {stat.value}
                    </div>
                    <div className='font-medium'>{stat.label}</div>
                    <div className='text-muted-foreground text-xs'>
                      {stat.detail}
                    </div>
                  </CardContent>
                </Card>
              </button>
            ))}
          </div>

          <Card className='py-3'>
            <CardContent className='flex flex-col gap-3 lg:flex-row lg:items-center'>
              <div className='text-muted-foreground flex items-center gap-2 text-sm font-medium'>
                <SlidersHorizontal className='size-4' />
                筛选
              </div>
              <div className='grid flex-1 grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-5'>
                <div className='relative'>
                  <Search className='text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2' />
                  <Input
                    aria-label='筛选模型名'
                    placeholder='筛选模型名'
                    className='pl-9'
                    value={fModel}
                    onChange={(event) => setFModel(event.target.value)}
                  />
                </div>
                <Input
                  aria-label='筛选渠道名'
                  placeholder='筛选渠道名'
                  value={fChannel}
                  onChange={(event) => setFChannel(event.target.value)}
                />
                <Select
                  value={fDelta}
                  onValueChange={(value) => value && setFDelta(value)}
                >
                  <SelectTrigger className='w-full'>
                    <SelectValue placeholder='偏移' />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='all'>全部偏移</SelectItem>
                    <SelectItem value='neg'>仅降权</SelectItem>
                    <SelectItem value='pos'>仅升权</SelectItem>
                    <SelectItem value='adjusted'>仅已调整</SelectItem>
                    <SelectItem value='zero'>仅无变化</SelectItem>
                  </SelectContent>
                </Select>
                <Select value={fRouting} onValueChange={(value) => value && setFRouting(value)}>
                  <SelectTrigger className='w-full'><SelectValue placeholder='路由状态' /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value='all'>全部路由状态</SelectItem>
                    <SelectItem value='quarantined'>仅看隔离</SelectItem>
                    <SelectItem value='canary'>仅看 Canary</SelectItem>
                    <SelectItem value='healthy'>仅看健康</SelectItem>
                    <SelectItem value='observe'>仅看观察中</SelectItem>
                  </SelectContent>
                </Select>
                <Select
                  value={fEnabled}
                  onValueChange={(value) => value && setFEnabled(value)}
                >
                  <SelectTrigger className='w-full'>
                    <SelectValue placeholder='状态' />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='all'>全部状态</SelectItem>
                    <SelectItem value='on'>仅启用</SelectItem>
                    <SelectItem value='off'>仅禁用</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <Badge variant='outline'>{filtered.length} 条结果</Badge>
            </CardContent>
          </Card>

          {isError ? (
            <Alert variant='destructive'>
              <AlertCircle />
              <AlertTitle>优先级数据加载失败</AlertTitle>
              <AlertDescription className='space-y-3'>
                <p>
                  {error instanceof Error
                    ? error.message
                    : '请检查网络或后端服务后重试。'}
                </p>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={isFetching}
                  onClick={() => void refetch()}
                >
                  <RefreshCw className={isFetching ? 'animate-spin' : ''} />
                  {isFetching ? '重试中' : '重新加载'}
                </Button>
              </AlertDescription>
            </Alert>
          ) : null}

          <div className='grid gap-3 md:hidden'>
            {isLoading ? (
              <Card>
                <CardContent className='text-muted-foreground py-8 text-center'>
                  加载中...
                </CardContent>
              </Card>
            ) : null}
            {!isLoading && !isError && filtered.length === 0 ? (
              <Card>
                <CardContent className='text-muted-foreground py-8 text-center'>
                  无匹配数据
                </CardContent>
              </Card>
            ) : null}
            {!isLoading && !isError
              ? filtered.map((row) => (
                  <Card key={`${row.channel_id}-${row.model}`} className='py-3'>
                    <CardContent className='space-y-3'>
                      <div className='flex items-start justify-between gap-3'>
                        <div className='min-w-0'>
                          <div className='truncate font-mono text-sm font-medium'>
                            {row.model}
                          </div>
                          <div className='text-muted-foreground truncate text-xs'>
                            #{row.channel_id} · {row.channel_name}
                          </div>
                        </div>
                        {row.routing_status === 'quarantined' || row.routing_status === 'canary' ? (
                          <QuarantineDetailsDialog channelName={row.channel_name} rows={[row]} trigger={<button type='button' aria-label={`${row.model} 隔离详情`}><RoutingStatusBadge row={row} /></button>} />
                        ) : <RoutingStatusBadge row={row} />}
                      </div>
                      <div className='grid grid-cols-4 gap-2 text-center text-xs'>
                        <div>
                          <div className='text-muted-foreground'>基准</div>
                          <div className='mt-1 font-semibold'>
                            {row.base_priority}
                          </div>
                        </div>
                        <div>
                          <div className='text-muted-foreground'>有效</div>
                          <div className='mt-1 font-semibold'>
                            {row.eff_priority}
                          </div>
                        </div>
                        <div>
                          <div className='text-muted-foreground'>偏移</div>
                          <div className='mt-1'>
                            <DeltaValue delta={row.delta} />
                          </div>
                        </div>
                        <div>
                          <div className='text-muted-foreground'>权重</div>
                          <div className='mt-1 font-semibold'>{row.weight}</div>
                        </div>
                      </div>
                    </CardContent>
                  </Card>
                ))
              : null}
          </div>

          <div className='hidden overflow-x-auto rounded-xl border md:block'>
            <Table className='min-w-[860px]'>
              <TableHeader>
                <TableRow>
                  {renderSortHead('#', 'channel_id')}
                  {renderSortHead('渠道', 'channel_name')}
                  {renderSortHead('模型', 'model')}
                  {renderSortHead('基准', 'base_priority')}
                  {renderSortHead('有效值', 'eff_priority')}
                  {renderSortHead('偏移', 'delta')}
                  {renderSortHead('权重', 'weight')}
                  <TableHead>状态</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {isLoading ? (
                  <TableRow>
                    <TableCell
                      colSpan={8}
                      className='text-muted-foreground py-10 text-center'
                    >
                      加载中...
                    </TableCell>
                  </TableRow>
                ) : null}
                {!isLoading && !isError && filtered.length === 0 ? (
                  <TableRow>
                    <TableCell
                      colSpan={8}
                      className='text-muted-foreground py-10 text-center'
                    >
                      无匹配数据
                    </TableCell>
                  </TableRow>
                ) : null}
                {!isLoading && !isError
                  ? filtered.map((row) => (
                      <TableRow key={`${row.channel_id}-${row.model}`}>
                        <TableCell>{row.channel_id}</TableCell>
                        <TableCell className='font-medium'>
                          {row.channel_name}
                        </TableCell>
                        <TableCell className='font-mono text-sm'>
                          {row.model}
                        </TableCell>
                        <TableCell>{row.base_priority}</TableCell>
                        <TableCell className='font-semibold'>
                          {row.eff_priority}
                        </TableCell>
                        <TableCell>
                          <DeltaValue delta={row.delta} />
                        </TableCell>
                        <TableCell>{row.weight}</TableCell>
                        <TableCell>
                          {row.routing_status === 'quarantined' || row.routing_status === 'canary' ? (
                            <QuarantineDetailsDialog channelName={row.channel_name} rows={[row]} trigger={<button type='button' aria-label={`${row.model} 隔离详情`}><RoutingStatusBadge row={row} /></button>} />
                          ) : <RoutingStatusBadge row={row} />}
                        </TableCell>
                      </TableRow>
                    ))
                  : null}
              </TableBody>
            </Table>
          </div>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
