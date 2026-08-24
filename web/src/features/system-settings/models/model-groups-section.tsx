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
// [CUSTOM] 需求5 模型分级管理：虚拟模型组可视化编辑器
// 数据源为 Option「ModelGroups」：{"top":["gpt-4o","claude-x"],"low":["qwen-turbo"]}
// 组内成员顺序 = 兜底优先级；运行时按滚动成功率动态插队（与需求4共用统计源）；
// 计费始终按实际命中的成员模型结算。

import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

type GroupRow = { key: number; name: string; membersText: string }

let rowKeySeq = 1

function parseModelGroups(raw: unknown): GroupRow[] {
  const rows: GroupRow[] = []
  let obj: Record<string, unknown> = {}
  try {
    const parsed = JSON.parse(
      typeof raw === 'string' ? raw || '{}' : '{}'
    ) as unknown
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      obj = parsed as Record<string, unknown>
    }
  } catch {
    // fall through with empty object
  }
  for (const [name, members] of Object.entries(obj)) {
    const list = Array.isArray(members)
      ? members.map((m) => String(m))
      : typeof members === 'string' && members.trim()
        ? [members.trim()]
        : []
    rows.push({
      key: rowKeySeq++,
      name,
      membersText: list.join(', '),
    })
  }
  return rows
}

function splitMembers(membersText: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const part of membersText.split(/[\n,，、]/)) {
    const m = part.trim()
    if (!m || seen.has(m)) continue
    seen.add(m)
    out.push(m)
  }
  return out
}

function serializeGroups(rows: GroupRow[]): string {
  const obj: Record<string, string[]> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (!name) continue
    obj[name] = splitMembers(row.membersText).filter((m) => m !== name)
  }
  return JSON.stringify(obj)
}

type ModelGroupsSectionProps = {
  defaultValues: { ModelGroups?: string }
}

export function ModelGroupsSection({ defaultValues }: ModelGroupsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const initialRaw =
    defaultValues && typeof defaultValues.ModelGroups === 'string'
      ? defaultValues.ModelGroups
      : '{}'
  const initialCanonical = useMemo(() => {
    const obj: Record<string, string[]> = {}
    for (const row of parseModelGroups(initialRaw)) {
      if (row.name.trim()) obj[row.name.trim()] = splitMembers(row.membersText)
    }
    return JSON.stringify(obj)
  }, [initialRaw])

  const [rows, setRows] = useState<GroupRow[]>(() =>
    parseModelGroups(initialRaw)
  )

  const currentCanonical = useMemo(() => serializeGroups(rows), [rows])
  const isDirty = currentCanonical !== initialCanonical

  const patchRow = (key: number, patch: Partial<Omit<GroupRow, 'key'>>) => {
    setRows((prev) =>
      prev.map((row) => (row.key === key ? { ...row, ...patch } : row))
    )
  }

  const moveRow = (index: number, direction: -1 | 1) => {
    setRows((prev) => {
      const target = index + direction
      if (target < 0 || target >= prev.length) return prev
      const next = [...prev]
      ;[next[index], next[target]] = [next[target], next[index]]
      return next
    })
  }

  const handleSave = async () => {
    const seenNames = new Set<string>()
    for (const row of rows) {
      const name = row.name.trim()
      if (!name) {
        toast.error(t('Every group needs a name'))
        return
      }
      if (seenNames.has(name)) {
        toast.error(
          t('Duplicate group name: {{name}}', { name })
        )
        return
      }
      seenNames.add(name)
      const members = splitMembers(row.membersText).filter((m) => m !== name)
      if (members.length === 0) {
        toast.error(
          t('Group {{name}} needs at least one valid member model', {
            name,
          })
        )
        return
      }
    }
    try {
      await updateOption.mutateAsync({
        key: 'ModelGroups',
        value: serializeGroups(rows),
      })
      toast.success(t('Model groups saved'))
    } catch {
      // mutation 层已有错误提示
    }
  }

  return (
    <SettingsSection title={t('Model Groups')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Virtual model aliases for tiered routing: requests to a group name rotate through its member models within the retry budget, billing follows the actual member served, and members are dynamically reordered by rolling success rate.'
        )}
      </p>
      <FormNavigationGuard when={isDirty} />
      <FormDirtyIndicator isDirty={isDirty} />
      <SettingsPageFormActions
        onSave={handleSave}
        isSaving={updateOption.isPending}
      />

      <div className='flex flex-col gap-3'>
        {rows.length === 0 ? (
          <div className='text-muted-foreground rounded-md border border-dashed p-4 text-sm'>
            {t(
              'No model groups yet. Example: {"top": ["gpt-4o", "claude-sonnet-4"]}'
            )}
          </div>
        ) : null}

        {rows.map((row, index) => (
          <div
            key={row.key}
            className='rounded-lg border p-3'
          >
            <div className='flex items-center gap-2'>
              <Input
                value={row.name}
                onChange={(e) => patchRow(row.key, { name: e.target.value })}
                placeholder={t('Group name (e.g. top)')}
                className='max-w-[220px] font-medium'
              />
              <div className='ml-auto flex items-center gap-1'>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  disabled={index === 0}
                  onClick={() => moveRow(index, -1)}
                  aria-label={t('Move up')}
                >
                  <ArrowUp />
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  disabled={index === rows.length - 1}
                  onClick={() => moveRow(index, 1)}
                  aria-label={t('Move down')}
                >
                  <ArrowDown />
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  onClick={() =>
                    setRows((prev) => prev.filter((r) => r.key !== row.key))
                  }
                  aria-label={t('Remove group')}
                >
                  <Trash2 />
                </Button>
              </div>
            </div>
            <Input
              value={row.membersText}
              onChange={(e) =>
                patchRow(row.key, { membersText: e.target.value })
              }
              placeholder='gpt-4o, claude-sonnet-4, qwen-max'
              className='mt-2'
            />
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(
                'Comma-separated member models in fallback priority order. Success rate automatically reorders them at runtime.'
              )}
            </p>
          </div>
        ))}

        <Button
          type='button'
          variant='outline'
          size='sm'
          className='self-start'
          onClick={() =>
            setRows((prev) => [
              ...prev,
              { key: rowKeySeq++, name: '', membersText: '' },
            ])
          }
        >
          <Plus />
          {t('Add Group')}
        </Button>
      </div>
    </SettingsSection>
  )
}
