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
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import {
  type SortingState,
  type VisibilityState,
  getCoreRowModel,
  getFacetedRowModel,
  getFacetedUniqueValues,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { useMediaQuery } from '@/hooks'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { dateToUnixTimestamp } from '@/lib/time'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { CompactDateTimeRangePicker } from '@/components/compact-date-time-range-picker'
import {
  DISABLED_ROW_DESKTOP,
  DISABLED_ROW_MOBILE,
  DataTablePage,
} from '@/components/data-table'
import {
  getEnabledModels,
  getUserUsageUsers,
  getUsers,
  searchUsers,
} from '../api'
import {
  USER_STATUS,
  getUserStatusOptions,
  getUserRoleOptions,
  isUserDeleted,
} from '../constants'
import type { User } from '../types'
import { DataTableBulkActions } from './data-table-bulk-actions'
import { useUsersColumns } from './users-columns'
import { useUsers } from './users-provider'

const route = getRouteApi('/_authenticated/users/')

type UserUsageFilters = {
  start?: Date
  end?: Date
  modelName: string
}

function isDisabledUserRow(user: User) {
  return isUserDeleted(user) || user.status === USER_STATUS.DISABLED
}

function getDateRangeDayCount(start?: Date, end?: Date) {
  if (!start || !end) return null
  const diff = end.getTime() - start.getTime()
  if (!Number.isFinite(diff) || diff < 0) return 1
  return Math.max(1, Math.ceil((diff + 1) / (24 * 60 * 60 * 1000)))
}

export function UsersTable() {
  const { t } = useTranslation()
  const columns = useUsersColumns()
  const { refreshTrigger } = useUsers()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const [rowSelection, setRowSelection] = useState({})
  const [sorting, setSorting] = useState<SortingState>([])
  const [columnVisibility, setColumnVisibility] = useState<VisibilityState>({})
  const [usageFilters, setUsageFilters] = useState<UserUsageFilters>({
    start: undefined,
    end: undefined,
    modelName: '',
  })

  const {
    globalFilter,
    onGlobalFilterChange,
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: { defaultPage: 1, defaultPageSize: isMobile ? 10 : 20 },
    globalFilter: { enabled: true, key: 'filter' },
    columnFilters: [
      { columnId: 'status', searchKey: 'status', type: 'array' },
      { columnId: 'role', searchKey: 'role', type: 'array' },
      { columnId: 'group', searchKey: 'group', type: 'string' },
    ],
  })

  const { data: enabledModels } = useQuery({
    queryKey: ['enabled-models'],
    queryFn: getEnabledModels,
    select: (res) => (res.success && Array.isArray(res.data) ? res.data : []),
    staleTime: 5 * 60_000,
  })

  const modelOptions =
    enabledModels?.map((model) => ({ label: model, value: model })) ?? []

  const startTimestamp = usageFilters.start
    ? dateToUnixTimestamp(usageFilters.start)
    : undefined
  const endTimestamp = usageFilters.end
    ? dateToUnixTimestamp(usageFilters.end)
    : undefined
  const usageRangeDays = getDateRangeDayCount(
    usageFilters.start,
    usageFilters.end
  )
  const hasUsageFilters = Boolean(
    usageFilters.start || usageFilters.end || usageFilters.modelName.trim()
  )
  const modelName = usageFilters.modelName.trim()
  const groupFilter = String(
    columnFilters.find((filter) => filter.id === 'group')?.value || ''
  ).trim()

  // Fetch data with React Query
  const { data, isLoading, isFetching } = useQuery({
    queryKey: [
      'users',
      pagination.pageIndex + 1,
      pagination.pageSize,
      globalFilter,
      startTimestamp,
      endTimestamp,
      modelName,
      hasUsageFilters,
      usageRangeDays,
      groupFilter,
      refreshTrigger,
    ],
    queryFn: async () => {
      const hasFilter = globalFilter?.trim()
      const params = {
        p: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
      }

      if (hasUsageFilters) {
        const result = await getUserUsageUsers({
          ...params,
          keyword: hasFilter || undefined,
          group: groupFilter || undefined,
          start_timestamp: startTimestamp,
          end_timestamp: endTimestamp,
          model_name: modelName || undefined,
        })

        if (!result.success) {
          toast.error(result.message || 'Failed to load user usage')
          return { items: [], total: 0 }
        }

        const items = result.data?.items || []
        return {
          items: items.map((user) => {
            const usageQuota = Number(user.usage_quota) || 0
            return {
              ...user,
              usage_quota: usageQuota,
              usage_token_used: Number(user.usage_token_used) || 0,
              usage_count: Number(user.usage_count) || 0,
              usage_daily_average_quota:
                typeof user.usage_daily_average_quota === 'number'
                  ? user.usage_daily_average_quota
                  : usageRangeDays
                    ? usageQuota / usageRangeDays
                    : undefined,
            }
          }),
          total: result.data?.total || 0,
        }
      }

      const result = hasFilter
        ? await searchUsers({ ...params, keyword: globalFilter })
        : await getUsers(params)

      if (!result.success) {
        toast.error(
          result.message || `Failed to ${hasFilter ? 'search' : 'load'} users`
        )
        return { items: [], total: 0 }
      }

      const items = result.data?.items || []
      return {
        items,
        total: result.data?.total || 0,
      }
    },
    placeholderData: (previousData) => previousData,
  })

  const users = data?.items || []

  const table = useReactTable({
    data: users,
    columns,
    state: {
      sorting,
      columnVisibility,
      rowSelection,
      columnFilters,
      globalFilter,
      pagination,
    },
    enableRowSelection: true,
    onRowSelectionChange: setRowSelection,
    onSortingChange: setSorting,
    onColumnVisibilityChange: setColumnVisibility,
    globalFilterFn: (row, _columnId, filterValue) => {
      const searchValue = String(filterValue).toLowerCase()
      const fields = [
        row.getValue('username'),
        row.original.display_name,
        row.original.email,
      ]
      return fields.some((field) =>
        String(field || '')
          .toLowerCase()
          .includes(searchValue)
      )
    },
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFacetedRowModel: getFacetedRowModel(),
    getFacetedUniqueValues: getFacetedUniqueValues(),
    onPaginationChange,
    onGlobalFilterChange,
    onColumnFiltersChange,
    manualPagination: !globalFilter,
    pageCount: Math.ceil((data?.total || 0) / pagination.pageSize),
  })

  const pageCount = table.getPageCount()
  useEffect(() => {
    ensurePageInRange(pageCount)
  }, [pageCount, ensurePageInRange])

  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={isLoading}
      isFetching={isFetching}
      emptyTitle={t('No Users Found')}
      emptyDescription={t(
        'No users available. Try adjusting your search or filters.'
      )}
      skeletonKeyPrefix='users-skeleton'
      toolbarProps={{
        searchPlaceholder: t('Filter by username, name or email...'),
        additionalSearch: (
          <>
            <CompactDateTimeRangePicker
              start={usageFilters.start}
              end={usageFilters.end}
              onChange={(range) =>
                setUsageFilters((prev) => ({
                  ...prev,
                  start: range.start,
                  end: range.end,
                }))
              }
              className='h-8 w-full sm:w-[260px] lg:w-[320px]'
            />
            <div className='w-full sm:w-[220px] lg:w-[260px]'>
              <ComboboxInput
                options={modelOptions}
                value={usageFilters.modelName}
                onValueChange={(value) =>
                  setUsageFilters((prev) => ({ ...prev, modelName: value }))
                }
                placeholder={t('Filter by model')}
                emptyText={t('No models found.')}
                allowCustomValue
                className='h-8'
              />
            </div>
          </>
        ),
        hasAdditionalFilters: hasUsageFilters,
        onReset: () =>
          setUsageFilters({
            start: undefined,
            end: undefined,
            modelName: '',
          }),
        filters: [
          {
            columnId: 'status',
            title: t('Status'),
            options: getUserStatusOptions(t),
            singleSelect: true,
          },
          {
            columnId: 'role',
            title: t('Role'),
            options: getUserRoleOptions(t),
            singleSelect: true,
          },
        ],
      }}
      getRowClassName={(row, { isMobile }) =>
        isDisabledUserRow(row.original)
          ? isMobile
            ? DISABLED_ROW_MOBILE
            : DISABLED_ROW_DESKTOP
          : undefined
      }
      bulkActions={<DataTableBulkActions table={table} />}
    />
  )
}
