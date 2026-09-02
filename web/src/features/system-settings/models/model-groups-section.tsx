/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
// [CUSTOM] Virtual model groups with live candidates and stale-member visibility.

import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowUp,
  Check,
  CloudDownload,
  Plus,
  Search,
  Trash2,
  TriangleAlert,
  X,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { requireSuccessfulResponse } from '@/lib/api-response'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  fetchUpstreamModels,
  getChannels,
  getEnabledModels,
} from '@/features/channels/api'

import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { getSystemOptions, updateSystemOption } from '../api'
import {
  appendGroupMembers,
  removeUnavailableMembers,
  splitMembers,
} from './model-groups-utils'

type GroupRow = { key: number; name: string; membersText: string }

let rowKeySeq = 1

function membersToText(members: unknown): string {
  if (Array.isArray(members)) return members.map(String).join(', ')
  if (typeof members === 'string') return members
  return ''
}

function parseModelGroups(raw: unknown): GroupRow[] {
  if (typeof raw !== 'string') {
    throw new Error('ModelGroups must be a JSON string')
  }
  const parsed: unknown = JSON.parse(raw || '{}')
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('ModelGroups must be a JSON object')
  }
  const object = parsed as Record<string, unknown>
  for (const [name, members] of Object.entries(object)) {
    if (!name || !Array.isArray(members) || members.some((item) => typeof item !== 'string')) {
      throw new Error(`Invalid model group: ${name || '(empty)'}`)
    }
  }
  return Object.entries(object).map(([name, members]) => ({
    key: rowKeySeq++,
    name,
    membersText: membersToText(members),
  }))
}

function tryParseModelGroups(raw: unknown):
  | { rows: GroupRow[]; error: null }
  | { rows: []; error: string } {
  try {
    return { rows: parseModelGroups(raw), error: null }
  } catch (error) {
    return {
      rows: [],
      error: error instanceof Error ? error.message : 'Invalid ModelGroups',
    }
  }
}

function serializeGroups(rows: GroupRow[]): string {
  const object: Record<string, string[]> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (!name) continue
    object[name] = splitMembers(row.membersText).filter(
      (model) => model !== name
    )
  }
  return JSON.stringify(object)
}

type ModelGroupsSectionProps = {
  defaultValues: { ModelGroups?: string }
}

export function ModelGroupsSection({ defaultValues }: ModelGroupsSectionProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [isSaving, setIsSaving] = useState(false)
  const initialRaw =
    typeof defaultValues?.ModelGroups === 'string'
      ? defaultValues.ModelGroups
      : '{}'
  const initialParse = useMemo(() => tryParseModelGroups(initialRaw), [initialRaw])
  const initialCanonical = useMemo(
    () => initialParse.error ? initialRaw : serializeGroups(initialParse.rows),
    [initialParse, initialRaw]
  )
  const [savedCanonical, setSavedCanonical] = useState(initialCanonical)
  const [rows, setRows] = useState<GroupRow[]>(() => initialParse.rows)
  const [search, setSearch] = useState('')
  const [selectedChannelId, setSelectedChannelId] = useState('')
  const [upstreamCandidates, setUpstreamCandidates] = useState<string[]>([])
  const [loadingUpstream, setLoadingUpstream] = useState(false)

  const enabledModelsQuery = useQuery({
    queryKey: ['enabled-models'],
    queryFn: async () =>
      requireSuccessfulResponse(
        await getEnabledModels(),
        t('Failed to load enabled models')
      ),
  })
  const channelsQuery = useQuery({
    queryKey: ['virtual-group-channel-candidates'],
    queryFn: async () =>
      requireSuccessfulResponse(
        await getChannels({ p: 1, page_size: 1000, status: 'enabled' }),
        t('Failed to load channels')
      ),
  })
  const liveModels = useMemo(
    () => new Set(enabledModelsQuery.data?.data || []),
    [enabledModelsQuery.data]
  )
  const liveModelsReady = enabledModelsQuery.isSuccess
  const candidates = useMemo(() => {
    const query = search.trim().toLowerCase()
    const merged = [...liveModels, ...upstreamCandidates]
    return [...new Set(merged)]
      .filter((model) => !query || model.toLowerCase().includes(query))
      .sort((a, b) => a.localeCompare(b))
      .slice(0, 200)
  }, [liveModels, search, upstreamCandidates])
  const channels = channelsQuery.data?.data?.items || []
  const currentCanonical = useMemo(() => serializeGroups(rows), [rows])
  const isDirty = currentCanonical !== savedCanonical

  useEffect(() => {
    if (initialParse.error || currentCanonical !== savedCanonical) return
    setRows(initialParse.rows)
    setSavedCanonical(serializeGroups(initialParse.rows))
  }, [initialRaw, initialCanonical]) // eslint-disable-line react-hooks/exhaustive-deps

  const patchRow = (key: number, patch: Partial<Omit<GroupRow, 'key'>>) => {
    setRows((previous) =>
      previous.map((row) => (row.key === key ? { ...row, ...patch } : row))
    )
  }
  const moveRow = (index: number, direction: -1 | 1) => {
    setRows((previous) => {
      const target = index + direction
      if (target < 0 || target >= previous.length) return previous
      const next = [...previous]
      ;[next[index], next[target]] = [next[target], next[index]]
      return next
    })
  }
  const fetchSelectedUpstream = async () => {
    if (!selectedChannelId) return
    setLoadingUpstream(true)
    try {
      const response = await fetchUpstreamModels(Number(selectedChannelId))
      if (!response.success) throw new Error(response.message || 'Fetch failed')
      const models = response.data || []
      setUpstreamCandidates(models)
      toast.success(
        t('Loaded {{count}} upstream models', { count: models.length })
      )
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to load models')
      )
    } finally {
      setLoadingUpstream(false)
    }
  }
  const handleSave = async () => {
    const names = new Set<string>()
    for (const row of rows) {
      const name = row.name.trim()
      if (!name) return toast.error(t('Every group needs a name'))
      if (names.has(name)) {
        return toast.error(t('Duplicate group name: {{name}}', { name }))
      }
      names.add(name)
    }
    for (const row of rows) {
      const members = splitMembers(row.membersText)
      const nested = members.filter((model) => names.has(model))
      if (nested.length > 0) {
        return toast.error(
          t(
            'Group {{name}} references another group as member: {{nested}}. Groups cannot be nested.',
            {
              name: row.name,
              nested: nested.join(', '),
            }
          )
        )
      }
      if (members.length === 0) {
        return toast.error(
          t('Group {{name}} needs at least one valid member model', {
            name: row.name,
          })
        )
      }
    }
    const submitted = serializeGroups(rows)
    setIsSaving(true)
    try {
      await updateSystemOption({
        key: 'ModelGroups',
        value: submitted,
      })
      const reloaded = await getSystemOptions()
      const persistedRaw = reloaded.data?.find(
        (option) => option.key === 'ModelGroups'
      )?.value
      if (typeof persistedRaw !== 'string') {
        throw new Error(t('Saved setting was not returned by the server'))
      }
      const persistedRows = parseModelGroups(persistedRaw)
      const persisted = serializeGroups(persistedRows)
      if (persisted !== submitted) {
        throw new Error(t('Saved setting does not match the submitted value'))
      }
      setRows(persistedRows)
      setSavedCanonical(persisted)
      await queryClient.invalidateQueries({ queryKey: ['system-options'] })
      toast.success(t('Model groups saved and verified'))
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to save model groups')
      )
    } finally {
      setIsSaving(false)
    }
  }

  if (initialParse.error) {
    return (
      <SettingsSection title={t('Model Groups')}>
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('Invalid saved model groups')}</AlertTitle>
          <AlertDescription>
            {t('Editing is disabled to prevent overwriting the saved value.')}
            <span className='mt-2 block font-mono text-xs'>
              {initialParse.error}
            </span>
          </AlertDescription>
        </Alert>
      </SettingsSection>
    )
  }

  return (
    <SettingsSection title={t('Model Groups')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Choose live channel models instead of typing names. Missing members are marked and never deleted without confirmation.'
        )}
      </p>
      <FormNavigationGuard when={isDirty} />
      <FormDirtyIndicator isDirty={isDirty} />
      <SettingsPageFormActions
        onSave={handleSave}
        isSaving={isSaving}
      />

      {(enabledModelsQuery.isError || channelsQuery.isError) && (
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('Model candidate data is unavailable')}</AlertTitle>
          <AlertDescription className='space-y-3'>
            <p>
              {t(
                'Saved group members are preserved and will not be marked unavailable until live model data loads successfully.'
              )}
            </p>
            <div className='flex flex-wrap gap-2'>
              {enabledModelsQuery.isError && (
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={enabledModelsQuery.isFetching}
                  onClick={() => void enabledModelsQuery.refetch()}
                >
                  {t('Retry model list')}
                </Button>
              )}
              {channelsQuery.isError && (
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={channelsQuery.isFetching}
                  onClick={() => void channelsQuery.refetch()}
                >
                  {t('Retry channel list')}
                </Button>
              )}
            </div>
          </AlertDescription>
        </Alert>
      )}

      <div className='bg-muted/20 grid gap-3 rounded-xl border p-3 lg:grid-cols-[minmax(0,1fr)_260px_auto]'>
        <div className='relative'>
          <Search className='text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2' />
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('Search available models')}
            className='pl-9'
          />
        </div>
        <Select
          value={selectedChannelId}
          disabled={channelsQuery.isLoading || channelsQuery.isError}
          onValueChange={(value) => setSelectedChannelId(value || '')}
        >
          <SelectTrigger>
            <SelectValue placeholder={t('Select a channel')} />
          </SelectTrigger>
          <SelectContent>
            {channels.map((channel) => (
              <SelectItem key={channel.id} value={String(channel.id)}>
                {channel.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          type='button'
          variant='outline'
          disabled={
            !selectedChannelId ||
            loadingUpstream ||
            channelsQuery.isLoading ||
            channelsQuery.isError
          }
          onClick={() => void fetchSelectedUpstream()}
        >
          <CloudDownload />
          {loadingUpstream ? t('Loading...') : t('Fetch upstream models')}
        </Button>
      </div>

      <div className='flex flex-col gap-3'>
        {rows.length === 0 ? (
          <div className='text-muted-foreground rounded-md border border-dashed p-4 text-sm'>
            {t('No model groups yet.')}
          </div>
        ) : null}
        {rows.map((row, index) => {
          const members = splitMembers(row.membersText)
          const stale = liveModelsReady
            ? members.filter((model) => !liveModels.has(model))
            : []
          return (
            <div key={row.key} className='space-y-3 rounded-xl border p-4'>
              <div className='flex items-center gap-2'>
                <Input
                  value={row.name}
                  onChange={(event) =>
                    patchRow(row.key, { name: event.target.value })
                  }
                  placeholder={t('Group name (e.g. top)')}
                  className='max-w-[220px] font-medium'
                />
                <Badge variant='outline'>
                  {members.length} {t('models')}
                </Badge>
                {stale.length > 0 ? (
                  <Badge variant='destructive'>
                    <TriangleAlert />
                    {stale.length} {t('unavailable')}
                  </Badge>
                ) : null}
                <div className='ml-auto flex gap-1'>
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
                      setRows((previous) =>
                        previous.filter((item) => item.key !== row.key)
                      )
                    }
                    aria-label={t('Remove group')}
                  >
                    <Trash2 />
                  </Button>
                </div>
              </div>

              <div className='flex flex-wrap gap-2'>
                {members.map((model) => {
                  const available = liveModels.has(model)
                  return (
                    <Badge
                      key={model}
                      variant={available ? 'secondary' : 'destructive'}
                      className='gap-1 py-1'
                    >
                      {available ? (
                        <Check className='size-3' />
                      ) : (
                        <TriangleAlert className='size-3' />
                      )}
                      {model}
                      <button
                        type='button'
                        aria-label={t('Remove {{model}}', { model })}
                        onClick={() =>
                          patchRow(row.key, {
                            membersText: members
                              .filter((item) => item !== model)
                              .join(', '),
                          })
                        }
                      >
                        <X className='size-3' />
                      </button>
                    </Badge>
                  )
                })}
              </div>

              <Input
                value={row.membersText}
                onChange={(event) =>
                  patchRow(row.key, { membersText: event.target.value })
                }
                placeholder={t('Manual model name, comma separated')}
              />

              <div className='max-h-52 overflow-auto rounded-lg border'>
                {candidates.map((model) => {
                  const selected = members.includes(model)
                  return (
                    <label
                      key={model}
                      className='hover:bg-muted flex cursor-pointer items-center gap-2 border-b px-3 py-2 text-sm last:border-b-0'
                    >
                      <Checkbox
                        checked={selected}
                        onCheckedChange={(checked) =>
                          patchRow(row.key, {
                            membersText: checked
                              ? appendGroupMembers(row.membersText, [model])
                              : members
                                  .filter((item) => item !== model)
                                  .join(', '),
                          })
                        }
                      />
                      <span className='min-w-0 flex-1 truncate font-mono'>
                        {model}
                      </span>
                      {upstreamCandidates.includes(model) &&
                      !liveModels.has(model) ? (
                        <Badge variant='outline'>{t('Upstream')}</Badge>
                      ) : null}
                    </label>
                  )
                })}
                {candidates.length === 0 ? (
                  <div className='text-muted-foreground p-3 text-sm'>
                    {t('No matching models')}
                  </div>
                ) : null}
              </div>

              {stale.length > 0 ? (
                <div className='flex items-center justify-between gap-3 rounded-lg border border-amber-400/40 bg-amber-500/5 p-3'>
                  <p className='text-sm'>
                    {t(
                      'Unavailable members stay saved but do not route. Review before removing them.'
                    )}
                  </p>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() =>
                      patchRow(row.key, {
                        membersText: removeUnavailableMembers(
                          row.membersText,
                          liveModels
                        ),
                      })
                    }
                  >
                    {t('Remove unavailable')}
                  </Button>
                </div>
              ) : null}
            </div>
          )
        })}
        <Button
          type='button'
          variant='outline'
          size='sm'
          className='self-start'
          onClick={() =>
            setRows((previous) => [
              ...previous,
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
