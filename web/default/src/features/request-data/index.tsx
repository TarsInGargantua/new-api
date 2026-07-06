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
import { useCallback, useMemo, useState, type KeyboardEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, getRouteApi } from '@tanstack/react-router'
import { Check, Copy, Database, Eye, RefreshCw, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { SectionPageLayout } from '@/components/layout'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'
import { getDefaultTimeRange } from '@/features/usage-logs/lib/utils'
import { formatLogQuota, formatTokens, formatUseTime } from '@/lib/format'

const route = getRouteApi('/_authenticated/request-data')

type RequestDataSearch = {
  page?: number
  pageSize?: number
  tokenName?: string
  modelName?: string
  username?: string
  startTime?: number
  endTime?: number
}

type APIRequestLogListItem = {
  id: number
  usage_log_id: number
  user_id: number
  username: string
  token_id: number
  token_name: string
  model_name: string
  created_at: number
  request_id?: string
  upstream_request_id?: string
  method: string
  request_path: string
  status_code: number
  is_stream: boolean
  channel_id: number
  group: string
  request_content_type: string
  response_content_type: string
  request_size: number
  response_size: number
  request_omitted_reason?: string
  response_omitted_reason?: string
  redacted: boolean
  usage?: APIRequestLogUsage
}

type APIRequestLogDetail = APIRequestLogListItem & {
  request_body?: string
  response_body?: string
  metadata?: string
}

type APIRequestLogUsage = {
  log_id: number
  created_at: number
  model_name: string
  token_name: string
  quota: number
  prompt_tokens: number
  completion_tokens: number
  token_used: number
  use_time: number
  content?: string
  other?: string
}

type PageData<T> = {
  items: T[]
  total: number
  page: number
  page_size: number
}

type APIResponse<T> = {
  success: boolean
  message?: string
  data?: T
}

function toSeconds(ms?: number) {
  return ms ? Math.floor(ms / 1000) : undefined
}

function buildRequestDataParams(search: RequestDataSearch) {
  const params = new URLSearchParams()
  const page = search.page || 1
  const pageSize = search.pageSize || 20
  params.set('p', String(page))
  params.set('page_size', String(pageSize))
  if (search.tokenName) params.set('token_name', search.tokenName)
  if (search.modelName) params.set('model_name', search.modelName)
  if (search.username) params.set('username', search.username)
  const startTimestamp = toSeconds(search.startTime)
  const endTimestamp = toSeconds(search.endTime)
  if (startTimestamp) params.set('start_timestamp', String(startTimestamp))
  if (endTimestamp) params.set('end_timestamp', String(endTimestamp))
  return params
}

async function fetchRequestData(search: RequestDataSearch) {
  const params = buildRequestDataParams(search)
  const res = await api.get<APIResponse<PageData<APIRequestLogListItem>>>(
    `/api/request-log?${params}`
  )
  return res.data
}

async function fetchRequestDataDetail(id: number) {
  const res = await api.get<APIResponse<APIRequestLogDetail>>(
    `/api/request-log/${id}`
  )
  return res.data
}

function formatDate(timestamp: number) {
  if (!timestamp) return '-'
  return new Date(timestamp * 1000).toLocaleString()
}

function formatBytes(size?: number) {
  if (!size || size <= 0) return '0 B'
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function prettyText(value?: string) {
  if (!value) return ''
  const trimmed = value.trim()
  if (!trimmed) return ''
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return value
  }
}

function BodyPanel(props: {
  title: string
  contentType?: string
  size?: number
  omittedReason?: string
  body?: string
}) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const body = prettyText(props.body)
  const displayText = body || props.omittedReason || t('No data available')

  return (
    <div className='space-y-2'>
      <div className='sr-only'>{props.title}</div>
      <div className='flex flex-wrap items-center gap-2 text-xs'>
        <Badge variant='outline' className='rounded-md'>
          {props.contentType || t('Unknown')}
        </Badge>
        <span className='text-muted-foreground'>{formatBytes(props.size)}</span>
        {props.omittedReason && (
          <Badge variant='secondary' className='rounded-md'>
            {props.omittedReason}
          </Badge>
        )}
      </div>
      <div className='bg-muted/30 relative overflow-hidden rounded-md border'>
        <Button
          variant='ghost'
          size='icon-sm'
          className='absolute top-2 right-2 z-10'
          onClick={() => copyToClipboard(displayText)}
          aria-label={t('Copy to clipboard')}
          title={t('Copy to clipboard')}
        >
          {copiedText === displayText ? (
            <Check className='size-4 text-green-600' />
          ) : (
            <Copy className='size-4' />
          )}
        </Button>
        <ScrollArea className='h-[55dvh]'>
          <pre className='min-w-0 p-3 pr-11 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap'>
            {displayText}
          </pre>
        </ScrollArea>
      </div>
    </div>
  )
}

function UsagePanel(props: { usage?: APIRequestLogUsage }) {
  const { t } = useTranslation()
  const usage = props.usage
  const usageOther = usage?.other ? prettyText(usage.other) : ''

  if (!usage) {
    return (
      <div className='bg-muted/30 text-muted-foreground rounded-md border p-4 text-sm'>
        {t('No matching usage log')}
      </div>
    )
  }

  return (
    <div className='space-y-3'>
      <div className='grid gap-2 text-xs sm:grid-cols-2 lg:grid-cols-5'>
        <div className='bg-muted/30 rounded-md border p-2'>
          <div className='text-muted-foreground'>{t('Prompt Tokens')}</div>
          <div className='font-medium'>
            {formatTokens(usage.prompt_tokens || 0)}
          </div>
        </div>
        <div className='bg-muted/30 rounded-md border p-2'>
          <div className='text-muted-foreground'>{t('Completion Tokens')}</div>
          <div className='font-medium'>
            {formatTokens(usage.completion_tokens || 0)}
          </div>
        </div>
        <div className='bg-muted/30 rounded-md border p-2'>
          <div className='text-muted-foreground'>{t('Total tokens')}</div>
          <div className='font-medium'>
            {formatTokens(usage.token_used || 0)}
          </div>
        </div>
        <div className='bg-muted/30 rounded-md border p-2'>
          <div className='text-muted-foreground'>{t('Cost')}</div>
          <div className='font-medium'>{formatLogQuota(usage.quota || 0)}</div>
        </div>
        <div className='bg-muted/30 rounded-md border p-2'>
          <div className='text-muted-foreground'>{t('Duration')}</div>
          <div className='font-medium'>
            {formatUseTime(usage.use_time || 0)}
          </div>
        </div>
      </div>
      {usage.content && (
        <BodyPanel
          title={t('Usage Content')}
          contentType='text/plain'
          size={usage.content.length}
          body={usage.content}
        />
      )}
      {usageOther && (
        <BodyPanel
          title={t('Usage Metadata')}
          contentType='application/json'
          size={usageOther.length}
          body={usageOther}
        />
      )}
    </div>
  )
}

function DetailDialog(props: {
  log: APIRequestLogListItem | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const id = props.log?.id || 0
  const loadErrorMessage = t('Failed to load request data')
  const { data, isLoading } = useQuery({
    queryKey: ['request-data-detail', id, loadErrorMessage],
    queryFn: async () => {
      const result = await fetchRequestDataDetail(id)
      if (!result.success) {
        toast.error(result.message || loadErrorMessage)
        return null
      }
      return result.data || null
    },
    enabled: props.open && id > 0,
  })

  const detail = data || props.log
  const metadata = data?.metadata ? prettyText(data.metadata) : ''
  const hasRequestBody = !!data?.request_body
  const hasResponseBody = !!data?.response_body
  const hasMetadata = !!metadata
  const defaultTab = hasRequestBody
    ? 'request'
    : hasResponseBody
      ? 'response'
      : 'usage'

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='max-h-[92dvh] gap-3 sm:max-w-5xl'>
        <DialogHeader>
          <DialogTitle className='flex items-center gap-2'>
            <Database className='size-4' />
            {t('Request Data Detail')}
          </DialogTitle>
          <DialogDescription>
            {detail
              ? detail.request_id || detail.upstream_request_id || '-'
              : t('Loading...')}
          </DialogDescription>
        </DialogHeader>

        {isLoading || !detail ? (
          <div className='text-muted-foreground py-10 text-center text-sm'>
            {t('Loading...')}
          </div>
        ) : (
          <div className='space-y-3'>
            <div className='grid gap-2 text-xs sm:grid-cols-2 lg:grid-cols-3'>
              <div className='bg-muted/30 rounded-md border p-2'>
                <div className='text-muted-foreground'>{t('Token')}</div>
                <div className='truncate font-medium'>
                  {detail.token_name || '-'}
                </div>
              </div>
              <div className='bg-muted/30 rounded-md border p-2'>
                <div className='text-muted-foreground'>{t('Model')}</div>
                <div className='truncate font-medium'>
                  {detail.model_name || '-'}
                </div>
              </div>
              <div className='bg-muted/30 rounded-md border p-2'>
                <div className='text-muted-foreground'>{t('Request ID')}</div>
                <div className='truncate font-mono'>
                  {detail.request_id || '-'}
                </div>
              </div>
            </div>

            <Tabs key={`${id}-${defaultTab}`} defaultValue={defaultTab}>
              <TabsList>
                {hasRequestBody && (
                  <TabsTrigger value='request'>{t('Request')}</TabsTrigger>
                )}
                {hasResponseBody && (
                  <TabsTrigger value='response'>{t('Response')}</TabsTrigger>
                )}
                <TabsTrigger value='usage'>{t('Usage')}</TabsTrigger>
                {hasMetadata && (
                  <TabsTrigger value='metadata'>{t('Metadata')}</TabsTrigger>
                )}
              </TabsList>
              {hasRequestBody && (
                <TabsContent value='request'>
                  <BodyPanel
                    title={t('Request')}
                    contentType={detail.request_content_type}
                    size={detail.request_size}
                    body={data?.request_body || ''}
                  />
                </TabsContent>
              )}
              {hasResponseBody && (
                <TabsContent value='response'>
                  <BodyPanel
                    title={t('Response')}
                    contentType={detail.response_content_type}
                    size={detail.response_size}
                    body={data?.response_body || ''}
                  />
                </TabsContent>
              )}
              <TabsContent value='usage'>
                <UsagePanel usage={data?.usage || props.log?.usage} />
              </TabsContent>
              {hasMetadata && (
                <TabsContent value='metadata'>
                  <BodyPanel
                    title={t('Metadata')}
                    contentType='application/json'
                    size={metadata.length}
                    body={metadata}
                  />
                </TabsContent>
              )}
            </Tabs>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

export function RequestData() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = route.useSearch() as RequestDataSearch
  const loadErrorMessage = t('Failed to load request data')
  const defaultRange = useMemo(() => getDefaultTimeRange(), [])
  const [filters, setFilters] = useState({
    tokenName: search.tokenName || '',
    modelName: search.modelName || '',
    username: search.username || '',
    startTime: search.startTime
      ? new Date(search.startTime)
      : defaultRange.start,
    endTime: search.endTime ? new Date(search.endTime) : defaultRange.end,
  })
  const [selectedLog, setSelectedLog] = useState<APIRequestLogListItem | null>(
    null
  )

  const effectiveSearch: RequestDataSearch = {
    page: search.page || 1,
    pageSize: search.pageSize || 20,
    tokenName: search.tokenName,
    modelName: search.modelName,
    username: search.username,
    startTime: search.startTime || defaultRange.start.getTime(),
    endTime: search.endTime || defaultRange.end.getTime(),
  }

  const { data, isLoading, isFetching, refetch } = useQuery({
    queryKey: ['request-data', effectiveSearch, loadErrorMessage],
    queryFn: async () => {
      const result = await fetchRequestData(effectiveSearch)
      if (!result.success) {
        toast.error(result.message || loadErrorMessage)
        return { items: [], total: 0, page: 1, page_size: 20 }
      }
      return result.data || { items: [], total: 0, page: 1, page_size: 20 }
    },
  })

  const page = effectiveSearch.page || 1
  const pageSize = effectiveSearch.pageSize || 20
  const total = data?.total || 0
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  const logs = data?.items || []

  const updateSearch = useCallback(
    (next: RequestDataSearch) => {
      navigate({
        to: '/request-data',
        search: {
          page: next.page || 1,
          pageSize: next.pageSize || pageSize,
          tokenName: next.tokenName || undefined,
          modelName: next.modelName || undefined,
          username: next.username || undefined,
          startTime: next.startTime,
          endTime: next.endTime,
        },
      })
    },
    [navigate, pageSize]
  )

  const applyFilters = useCallback(() => {
    updateSearch({
      page: 1,
      pageSize,
      tokenName: filters.tokenName.trim(),
      modelName: filters.modelName.trim(),
      username: filters.username.trim(),
      startTime: filters.startTime?.getTime(),
      endTime: filters.endTime?.getTime(),
    })
  }, [filters, pageSize, updateSearch])

  const resetFilters = useCallback(() => {
    const range = getDefaultTimeRange()
    setFilters({
      tokenName: '',
      modelName: '',
      username: '',
      startTime: range.start,
      endTime: range.end,
    })
    updateSearch({
      page: 1,
      pageSize,
      startTime: range.start.getTime(),
      endTime: range.end.getTime(),
    })
  }, [pageSize, updateSearch])

  const handleKeyDown = useCallback(
    (event: KeyboardEvent) => {
      if (event.key === 'Enter') applyFilters()
    },
    [applyFilters]
  )

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>{t('Request Data')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button
            variant='outline'
            size='sm'
            onClick={() => refetch()}
            disabled={isFetching}
          >
            <RefreshCw className={cn('size-4', isFetching && 'animate-spin')} />
            {t('Refresh')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='space-y-3'>
            <div className='bg-muted/20 flex flex-wrap items-center gap-2 rounded-md border p-2'>
              <CompactDateTimeRangePicker
                start={filters.startTime}
                end={filters.endTime}
                onChange={({ start, end }) =>
                  setFilters((prev) => ({
                    ...prev,
                    startTime: start || prev.startTime,
                    endTime: end || prev.endTime,
                  }))
                }
                className='w-full sm:w-[340px]'
              />
              <Input
                placeholder={t('Token Name')}
                value={filters.tokenName}
                onChange={(event) =>
                  setFilters((prev) => ({
                    ...prev,
                    tokenName: event.target.value,
                  }))
                }
                onKeyDown={handleKeyDown}
                className='w-full sm:w-[160px]'
              />
              <Input
                placeholder={t('Model Name')}
                value={filters.modelName}
                onChange={(event) =>
                  setFilters((prev) => ({
                    ...prev,
                    modelName: event.target.value,
                  }))
                }
                onKeyDown={handleKeyDown}
                className='w-full sm:w-[160px]'
              />
              <Input
                placeholder={t('Username')}
                value={filters.username}
                onChange={(event) =>
                  setFilters((prev) => ({
                    ...prev,
                    username: event.target.value,
                  }))
                }
                onKeyDown={handleKeyDown}
                className='w-full sm:w-[160px]'
              />
              <Button size='sm' onClick={applyFilters}>
                <Search className='size-4' />
                {t('Search')}
              </Button>
              <Button variant='ghost' size='sm' onClick={resetFilters}>
                {t('Reset')}
              </Button>
            </div>

            <div className='overflow-hidden rounded-md border'>
              <Table>
                <TableHeader className='bg-muted/40'>
                  <TableRow>
                    <TableHead>{t('Time')}</TableHead>
                    <TableHead>{t('User')}</TableHead>
                    <TableHead>{t('Token')}</TableHead>
                    <TableHead>{t('Model')}</TableHead>
                    <TableHead>{t('Token Usage')}</TableHead>
                    <TableHead>{t('Actions')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {isLoading ? (
                    <TableRow>
                      <TableCell colSpan={6} className='h-28 text-center'>
                        {t('Loading...')}
                      </TableCell>
                    </TableRow>
                  ) : logs.length === 0 ? (
                    <TableRow>
                      <TableCell
                        colSpan={6}
                        className='text-muted-foreground h-28 text-center'
                      >
                        {t('No request data found')}
                      </TableCell>
                    </TableRow>
                  ) : (
                    logs.map((log) => (
                      <TableRow key={log.id}>
                        <TableCell className='font-mono text-xs'>
                          {formatDate(log.created_at)}
                        </TableCell>
                        <TableCell>
                          <div className='max-w-[120px] truncate'>
                            {log.username || `#${log.user_id}`}
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className='max-w-[160px] truncate font-mono text-xs'>
                            {log.token_name || '-'}
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className='max-w-[180px] truncate'>
                            {log.model_name || '-'}
                          </div>
                        </TableCell>
                        <TableCell>
                          {log.usage ? (
                            <div className='flex flex-col gap-0.5 font-mono text-xs'>
                              <span>
                                {formatTokens(log.usage.token_used || 0)}
                              </span>
                              <span className='text-muted-foreground'>
                                {formatLogQuota(log.usage.quota || 0)}
                              </span>
                            </div>
                          ) : (
                            <span className='text-muted-foreground'>-</span>
                          )}
                        </TableCell>
                        <TableCell>
                          <div className='flex items-center gap-1'>
                            <Button
                              variant='ghost'
                              size='icon-sm'
                              onClick={() => setSelectedLog(log)}
                              aria-label={t('View details')}
                              title={t('View details')}
                            >
                              <Eye className='size-4' />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </div>

            <div className='flex flex-wrap items-center justify-between gap-2 text-sm'>
              <div className='text-muted-foreground'>
                {t('Total')}: {total}
              </div>
              <div className='flex items-center gap-2'>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={page <= 1}
                  onClick={() =>
                    updateSearch({ ...effectiveSearch, page: page - 1 })
                  }
                >
                  {t('Previous')}
                </Button>
                <span className='text-muted-foreground min-w-20 text-center text-xs'>
                  {page} / {pageCount}
                </span>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={page >= pageCount}
                  onClick={() =>
                    updateSearch({ ...effectiveSearch, page: page + 1 })
                  }
                >
                  {t('Next')}
                </Button>
              </div>
            </div>
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <DetailDialog
        log={selectedLog}
        open={!!selectedLog}
        onOpenChange={(open) => {
          if (!open) setSelectedLog(null)
        }}
      />
    </>
  )
}
