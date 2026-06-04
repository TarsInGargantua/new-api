/*
Copyright (C) 2025 QuantumNous

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

import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { API, showError, showSuccess } from '../../helpers';
import { ITEMS_PER_PAGE } from '../../constants';
import { useTableCompactMode } from '../common/useTableCompactMode';

const toUnixTimestamp = (value) => {
  if (!value) return undefined;
  if (typeof value === 'number') {
    return value > 10_000_000_000 ? Math.floor(value / 1000) : value;
  }
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : Math.floor(parsed / 1000);
};

const getDateRangeDayCount = (startTimestamp, endTimestamp) => {
  if (!startTimestamp || !endTimestamp) return undefined;
  const diffSeconds = endTimestamp - startTimestamp;
  if (!Number.isFinite(diffSeconds) || diffSeconds < 0) return 1;
  return Math.max(1, Math.ceil((diffSeconds + 1) / 86400));
};

const addUsageSummary = (usageByUserId, item) => {
  const userId = item?.user_id;
  if (!userId) return;
  const prev = usageByUserId.get(userId) || {
    quota: 0,
    token_used: 0,
    count: 0,
  };
  usageByUserId.set(userId, {
    quota: prev.quota + (Number(item.quota) || 0),
    token_used: prev.token_used + (Number(item.token_used) || 0),
    count: prev.count + (Number(item.count) || 0),
  });
};

const buildModelOptions = (models) =>
  Array.from(new Set((models || []).map((model) => String(model || '').trim())))
    .filter(Boolean)
    .sort((a, b) => a.localeCompare(b))
    .map((model) => ({
      label: model,
      value: model,
    }));

export const useUsersData = () => {
  const { t } = useTranslation();
  const [compactMode, setCompactMode] = useTableCompactMode('users');

  // State management
  const [users, setUsers] = useState([]);
  const [loading, setLoading] = useState(true);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useState(ITEMS_PER_PAGE);
  const [searching, setSearching] = useState(false);
  const [groupOptions, setGroupOptions] = useState([]);
  const [enabledModelOptions, setEnabledModelOptions] = useState([]);
  const [userCount, setUserCount] = useState(0);

  // Modal states
  const [showAddUser, setShowAddUser] = useState(false);
  const [showEditUser, setShowEditUser] = useState(false);
  const [editingUser, setEditingUser] = useState({
    id: undefined,
  });

  // Form initial values
  const formInitValues = {
    searchKeyword: '',
    searchGroup: '',
    usageDateRange: [],
    usageModelName: '',
  };

  // Form API reference
  const [formApi, setFormApi] = useState(null);

  // Get form values helper function
  const getFormValues = () => {
    const formValues = formApi ? formApi.getValues() : {};
    const usageDateRange = Array.isArray(formValues.usageDateRange)
      ? formValues.usageDateRange
      : [];
    return {
      searchKeyword: formValues.searchKeyword || '',
      searchGroup: formValues.searchGroup || '',
      usageDateRange,
      usageModelName: formValues.usageModelName || '',
    };
  };

  const getUsageFilterParams = (values = getFormValues()) => {
    const [start, end] = values.usageDateRange || [];
    const startTimestamp = toUnixTimestamp(start);
    const endTimestamp = toUnixTimestamp(end);
    const modelName = String(values.usageModelName || '').trim();

    return {
      start_timestamp: startTimestamp,
      end_timestamp: endTimestamp,
      model_name: modelName,
      hasFilters: Boolean(startTimestamp || endTimestamp || modelName),
      dayCount: getDateRangeDayCount(startTimestamp, endTimestamp),
    };
  };

  const loadUsageForUsers = async (pageUsers, usageParams) => {
    const usageByUserId = new Map();
    if (!usageParams.hasFilters || pageUsers.length === 0) {
      return usageByUserId;
    }

    const userIds = pageUsers.map((user) => user.id).filter(Boolean);
    if (userIds.length === 0) return usageByUserId;

    const params = {
      user_ids: userIds.join(','),
    };
    if (usageParams.start_timestamp) {
      params.start_timestamp = usageParams.start_timestamp;
    }
    if (usageParams.end_timestamp) {
      params.end_timestamp = usageParams.end_timestamp;
    }
    if (usageParams.model_name) {
      params.model_name = usageParams.model_name;
    }

    const res = await API.get('/api/log/user_usage', { params });
    const { success, message, data } = res.data;
    if (!success) {
      showError(message);
      return usageByUserId;
    }

    (data || []).forEach((item) => addUsageSummary(usageByUserId, item));
    return usageByUserId;
  };

  // Set user format with key field and filtered usage summary
  const setUserFormat = async (users) => {
    const pageUsers = users.map((user) => ({
      ...user,
      key: user.id,
    }));
    const usageParams = getUsageFilterParams();
    const usageByUserId = await loadUsageForUsers(pageUsers, usageParams);

    setUsers(
      pageUsers.map((user) => {
        const usage = usageByUserId.get(user.id);
        const usageQuota = usage?.quota || 0;
        return {
          ...user,
          usage_has_filters: usageParams.hasFilters,
          usage_quota: usageQuota,
          usage_token_used: usage?.token_used || 0,
          usage_count: usage?.count || 0,
          usage_daily_average_quota: usageParams.dayCount
            ? usageQuota / usageParams.dayCount
            : undefined,
        };
      }),
    );
  };

  // Load users data
  const loadUsers = async (startIdx, pageSize) => {
    setLoading(true);
    try {
      const res = await API.get(
        `/api/user/?p=${startIdx}&page_size=${pageSize}`,
      );
      const { success, message, data } = res.data;
      if (success) {
        const newPageData = data.items || [];
        setActivePage(data.page);
        setUserCount(data.total);
        await setUserFormat(newPageData);
      } else {
        showError(message);
      }
    } finally {
      setLoading(false);
    }
  };

  // Search users with keyword and group
  const searchUsers = async (
    startIdx,
    pageSize,
    searchKeyword = null,
    searchGroup = null,
  ) => {
    // If no parameters passed, get values from form
    if (searchKeyword === null || searchGroup === null) {
      const formValues = getFormValues();
      searchKeyword = formValues.searchKeyword;
      searchGroup = formValues.searchGroup;
    }

    if (searchKeyword === '' && searchGroup === '') {
      // If keyword is blank, load files instead
      await loadUsers(startIdx, pageSize);
      return;
    }
    setSearching(true);
    try {
      const params = new URLSearchParams({
        keyword: searchKeyword,
        group: searchGroup,
        p: String(startIdx),
        page_size: String(pageSize),
      });
      const res = await API.get(`/api/user/search?${params.toString()}`);
      const { success, message, data } = res.data;
      if (success) {
        const newPageData = data.items || [];
        setActivePage(data.page);
        setUserCount(data.total);
        await setUserFormat(newPageData);
      } else {
        showError(message);
      }
    } finally {
      setSearching(false);
    }
  };

  // Manage user operations (promote, demote, enable, disable, delete)
  const manageUser = async (userId, action, record) => {
    // Trigger loading state to force table re-render
    setLoading(true);

    const res = await API.post('/api/user/manage', {
      id: userId,
      action,
    });

    const { success, message } = res.data;
    if (success) {
      showSuccess(t('操作成功完成！'));
      const user = res.data.data;

      // Create a new array and new object to ensure React detects changes
      const newUsers = users.map((u) => {
        if (u.id === userId) {
          if (action === 'delete') {
            return { ...u, DeletedAt: new Date() };
          }
          return { ...u, status: user.status, role: user.role };
        }
        return u;
      });

      setUsers(newUsers);
    } else {
      showError(message);
    }

    setLoading(false);
  };

  const resetUserPasskey = async (user) => {
    if (!user) {
      return;
    }
    try {
      const res = await API.delete(`/api/user/${user.id}/reset_passkey`);
      const { success, message } = res.data;
      if (success) {
        showSuccess(t('Passkey 已重置'));
      } else {
        showError(message || t('操作失败，请重试'));
      }
    } catch (error) {
      showError(t('操作失败，请重试'));
    }
  };

  const resetUserTwoFA = async (user) => {
    if (!user) {
      return;
    }
    try {
      const res = await API.delete(`/api/user/${user.id}/2fa`);
      const { success, message } = res.data;
      if (success) {
        showSuccess(t('二步验证已重置'));
      } else {
        showError(message || t('操作失败，请重试'));
      }
    } catch (error) {
      showError(t('操作失败，请重试'));
    }
  };

  // Handle page change
  const handlePageChange = (page) => {
    setActivePage(page);
    const { searchKeyword, searchGroup } = getFormValues();
    if (searchKeyword === '' && searchGroup === '') {
      loadUsers(page, pageSize).then();
    } else {
      searchUsers(page, pageSize, searchKeyword, searchGroup).then();
    }
  };

  // Handle page size change
  const handlePageSizeChange = async (size) => {
    localStorage.setItem('page-size', size + '');
    setPageSize(size);
    setActivePage(1);
    const { searchKeyword, searchGroup } = getFormValues();
    try {
      if (searchKeyword === '' && searchGroup === '') {
        await loadUsers(1, size);
      } else {
        await searchUsers(1, size, searchKeyword, searchGroup);
      }
    } catch (reason) {
      showError(reason);
    }
  };

  // Handle table row styling for disabled/deleted users
  const handleRow = (record, index) => {
    if (record.DeletedAt !== null || record.status !== 1) {
      return {
        style: {
          background: 'var(--semi-color-disabled-border)',
        },
      };
    } else {
      return {};
    }
  };

  // Refresh data
  const refresh = async (page = activePage) => {
    const { searchKeyword, searchGroup } = getFormValues();
    if (searchKeyword === '' && searchGroup === '') {
      await loadUsers(page, pageSize);
    } else {
      await searchUsers(page, pageSize, searchKeyword, searchGroup);
    }
  };

  // Fetch groups data
  const fetchGroups = async () => {
    try {
      let res = await API.get(`/api/group/`);
      if (res === undefined) {
        return;
      }
      setGroupOptions(
        res.data.data.map((group) => ({
          label: group,
          value: group,
        })),
      );
    } catch (error) {
      showError(error.message);
    }
  };

  const fetchEnabledModels = async () => {
    const results = await Promise.allSettled([
      API.get('/api/channel/models_enabled', { skipErrorHandler: true }),
      API.get('/api/log/models', { skipErrorHandler: true }),
    ]);
    const models = [];

    results.forEach((result) => {
      if (result.status !== 'fulfilled') return;
      const { success, data } = result.value.data || {};
      if (success && Array.isArray(data)) {
        models.push(...data);
      }
    });

    setEnabledModelOptions(buildModelOptions(models));
  };

  // Modal control functions
  const closeAddUser = () => {
    setShowAddUser(false);
  };

  const closeEditUser = () => {
    setShowEditUser(false);
    setEditingUser({
      id: undefined,
    });
  };

  // Initialize data on component mount
  useEffect(() => {
    loadUsers(0, pageSize)
      .then()
      .catch((reason) => {
        showError(reason);
      });
    fetchGroups().then();
    fetchEnabledModels().then();
  }, []);

  return {
    // Data state
    users,
    loading,
    activePage,
    pageSize,
    userCount,
    searching,
    groupOptions,
    enabledModelOptions,

    // Modal state
    showAddUser,
    showEditUser,
    editingUser,
    setShowAddUser,
    setShowEditUser,
    setEditingUser,

    // Form state
    formInitValues,
    formApi,
    setFormApi,

    // UI state
    compactMode,
    setCompactMode,

    // Actions
    loadUsers,
    searchUsers,
    manageUser,
    resetUserPasskey,
    resetUserTwoFA,
    handlePageChange,
    handlePageSizeChange,
    handleRow,
    refresh,
    closeAddUser,
    closeEditUser,
    getFormValues,
    getUsageFilterParams,

    // Translation
    t,
  };
};
