import { useQuery } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, Minus, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
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
import { getModelPriority, type ModelPriorityRow } from './api'

export function ModelPriority() {
  const { data: rows = [], isLoading, refetch } = useQuery({
    queryKey: ['model-priority'],
    queryFn: getModelPriority,
  })

  const [fModel, setFModel] = useState('')
  const [fChannel, setFChannel] = useState('')
  const [fDelta, setFDelta] = useState('all')
  const [fEnabled, setFEnabled] = useState('all')
  const [sortBy, setSortBy] = useState<keyof ModelPriorityRow>('model')
  const [sortDir, setSortDir] = useState(1)

  const stats = useMemo(() => ({
    total: rows.length,
    neg: rows.filter((r) => r.delta < 0).length,
    pos: rows.filter((r) => r.delta > 0).length,
    adjusted: rows.filter((r) => r.delta !== 0).length,
  }), [rows])

  const filtered = useMemo(() => {
    let d = rows.filter((r) => {
      if (fModel && !r.model.toLowerCase().includes(fModel.toLowerCase())) return false
      if (fChannel && !r.channel_name.toLowerCase().includes(fChannel.toLowerCase())) return false
      if (fDelta === 'neg' && r.delta >= 0) return false
      if (fDelta === 'pos' && r.delta <= 0) return false
      if (fDelta === 'zero' && r.delta !== 0) return false
      if (fEnabled === 'on' && !r.enabled) return false
      if (fEnabled === 'off' && r.enabled) return false
      return true
    })
    d = [...d].sort((a, b) => {
      const va = a[sortBy], vb = b[sortBy]
      if (typeof va === 'string') return va.localeCompare(vb as string) * sortDir
      return ((va as number) - (vb as number)) * sortDir
    })
    return d
  }, [rows, fModel, fChannel, fDelta, fEnabled, sortBy, sortDir])

  const sortCol = (k: keyof ModelPriorityRow) => {
    if (sortBy === k) setSortDir(-sortDir)
    else { setSortBy(k); setSortDir(1) }
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>模型优先级看板</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button variant="outline" size="sm" onClick={() => refetch()}>
          <RefreshCw className="h-4 w-4 mr-2" /> 刷新
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className="grid grid-cols-2 gap-3 mb-4 sm:grid-cols-4">
          {[
            { n: stats.total, l: '总条目', c: 'text-blue-600' },
            { n: stats.neg, l: '降权中', c: 'text-red-600' },
            { n: stats.pos, l: '升权中', c: 'text-green-600' },
            { n: stats.adjusted, l: '自动调整', c: 'text-amber-600' },
          ].map((s) => (
            <div key={s.l} className="rounded-lg border bg-card p-4">
              <div className={`text-2xl font-bold ${s.c}`}>{s.n}</div>
              <div className="text-xs text-muted-foreground mt-1">{s.l}</div>
            </div>
          ))}
        </div>

        <div className="flex gap-2 mb-4 flex-wrap">
          <Input placeholder="筛模型名..." className="w-40" value={fModel} onChange={(e) => setFModel(e.target.value)} />
          <Input placeholder="筛渠道名..." className="w-40" value={fChannel} onChange={(e) => setFChannel(e.target.value)} />
          <Select value={fDelta} onValueChange={setFDelta}>
            <SelectTrigger className="w-32"><SelectValue placeholder="偏移" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部偏移</SelectItem>
              <SelectItem value="neg">仅降权</SelectItem>
              <SelectItem value="pos">仅升权</SelectItem>
              <SelectItem value="zero">仅无变化</SelectItem>
            </SelectContent>
          </Select>
          <Select value={fEnabled} onValueChange={setFEnabled}>
            <SelectTrigger className="w-32"><SelectValue placeholder="状态" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              <SelectItem value="on">仅启用</SelectItem>
              <SelectItem value="off">仅禁用</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="cursor-pointer" onClick={() => sortCol('channel_id')}>#</TableHead>
                <TableHead className="cursor-pointer" onClick={() => sortCol('channel_name')}>渠道</TableHead>
                <TableHead className="cursor-pointer" onClick={() => sortCol('model')}>模型</TableHead>
                <TableHead className="cursor-pointer" onClick={() => sortCol('base_priority')}>基准</TableHead>
                <TableHead className="cursor-pointer" onClick={() => sortCol('eff_priority')}>有效值</TableHead>
                <TableHead className="cursor-pointer" onClick={() => sortCol('delta')}>偏移</TableHead>
                <TableHead className="cursor-pointer" onClick={() => sortCol('weight')}>权重</TableHead>
                <TableHead>状态</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading ? (
                <TableRow><TableCell colSpan={8} className="text-center py-8 text-muted-foreground">加载中...</TableCell></TableRow>
              ) : filtered.length === 0 ? (
                <TableRow><TableCell colSpan={8} className="text-center py-8 text-muted-foreground">无匹配数据</TableCell></TableRow>
              ) : filtered.map((r) => (
                <TableRow key={`${r.channel_id}-${r.model}`}>
                  <TableCell>{r.channel_id}</TableCell>
                  <TableCell>{r.channel_name}</TableCell>
                  <TableCell className="font-mono text-sm">{r.model}</TableCell>
                  <TableCell>{r.base_priority}</TableCell>
                  <TableCell className="font-semibold">{r.eff_priority}</TableCell>
                  <TableCell>
                    <span className={`inline-flex items-center gap-1 font-semibold ${
                      r.delta < 0 ? 'text-red-600' : r.delta > 0 ? 'text-green-600' : 'text-muted-foreground'
                    }`}>
                      {r.delta < 0 ? <ArrowDown className="h-3 w-3" /> : r.delta > 0 ? <ArrowUp className="h-3 w-3" /> : <Minus className="h-3 w-3" />}
                      {r.delta > 0 ? '+' : ''}{r.delta}
                    </span>
                  </TableCell>
                  <TableCell>{r.weight}</TableCell>
                  <TableCell>
                    <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${
                      r.enabled ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-700'
                    }`}>
                      {r.enabled ? '启用' : '禁用'}
                    </span>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
