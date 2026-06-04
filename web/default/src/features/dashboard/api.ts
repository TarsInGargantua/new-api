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
import { api } from '@/lib/api'
import type { QuotaDataItem, UptimeGroupResult } from './types'

// ============================================================================
// Dashboard APIs
// ============================================================================

// ----------------------------------------------------------------------------
// Quota & Usage Data
// ----------------------------------------------------------------------------

// Get user quota data within a time range
// Admin users get all users' data by default (matching classic frontend behavior)
export async function getUserQuotaDates(
  params: {
    start_timestamp: number
    end_timestamp: number
    default_time?: string
    username?: string
  },
  isAdmin = false
) {
  const endpoint = isAdmin ? '/api/data' : '/api/data/self'
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    endpoint,
    { params }
  )
  return res.data
}

// ----------------------------------------------------------------------------
// System Monitoring
// ----------------------------------------------------------------------------

export async function getUserQuotaDataByUsers(params: {
  start_timestamp: number
  end_timestamp: number
}) {
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    '/api/data/users',
    { params }
  )
  return res.data
}

export async function getUserDailyUsageStats(params: {
  start_timestamp?: number
  end_timestamp?: number
  model_name?: string
}) {
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    '/api/log/user_daily_usage',
    { params }
  )
  return res.data
}

export async function getEnabledModels() {
  type ModelsResponse = {
    success: boolean
    message?: string
    data?: unknown[]
  }
  const modelNameFromItem = (model: unknown) => {
    if (typeof model === 'string') return model
    if (model && typeof model === 'object') {
      const item = model as {
        id?: unknown
        model_name?: unknown
        name?: unknown
      }
      return item.id || item.model_name || item.name || ''
    }
    return ''
  }
  const results = await Promise.allSettled([
    api.get<ModelsResponse>('/api/channel/models_enabled'),
    api.get<ModelsResponse>('/api/log/models'),
    api.get<ModelsResponse>('/api/channel/models'),
  ])
  const models = new Set<string>()
  let message = ''

  for (const result of results) {
    if (result.status !== 'fulfilled') continue
    const payload = result.value.data
    if (!payload.success) {
      message ||= payload.message || ''
      continue
    }
    for (const model of payload.data || []) {
      const normalized = String(modelNameFromItem(model)).trim()
      if (normalized) models.add(normalized)
    }
  }

  return {
    success: models.size > 0 || results.some((r) => r.status === 'fulfilled'),
    message,
    data: Array.from(models).sort((a, b) => a.localeCompare(b)),
  }
}

// Get uptime monitoring status for all services
export async function getUptimeStatus() {
  const res = await api.get<{ success: boolean; data: UptimeGroupResult[] }>(
    '/api/uptime/status'
  )
  return res.data
}
