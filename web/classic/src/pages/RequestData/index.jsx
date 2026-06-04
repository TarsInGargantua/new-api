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

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Button,
  Empty,
  Form,
  Modal,
  Space,
  TabPane,
  Tabs,
  Tag,
  Typography,
} from '@douyinfe/semi-ui';
import { IconSearch } from '@douyinfe/semi-icons';
import { Copy, Database, Eye, RefreshCw, ShieldCheck } from 'lucide-react';
import CardPro from '../../components/common/ui/CardPro';
import CardTable from '../../components/common/ui/CardTable';
import { DATE_RANGE_PRESETS } from '../../constants/console.constants';
import { useIsMobile } from '../../hooks/common/useIsMobile';
import {
  API,
  copy,
  showError,
  showSuccess,
  timestamp2string,
} from '../../helpers';
import { createCardProPagination } from '../../helpers/utils';

const PAGE_SIZE = 20;

const formatBytes = (size) => {
  const value = Number(size) || 0;
  if (value <= 0) return '0 B';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
};

const statusColor = (statusCode) => {
  if (statusCode >= 200 && statusCode < 300) return 'green';
  if (statusCode >= 400) return 'red';
  return 'orange';
};

const toUnixTimestamp = (value) => {
  if (!value) return undefined;
  if (typeof value === 'number') {
    return value > 10_000_000_000 ? Math.floor(value / 1000) : value;
  }
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : Math.floor(parsed / 1000);
};

const prettyText = (value) => {
  if (!value) return '';
  const trimmed = String(value).trim();
  if (!trimmed) return '';
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return String(value);
  }
};

const BodyPanel = ({ title, contentType, size, omittedReason, body, t }) => {
  const displayText = prettyText(body) || omittedReason || t('无内容');

  const handleCopy = async () => {
    if (await copy(displayText)) {
      showSuccess(t('复制成功'));
    } else {
      showError(t('复制失败'));
    }
  };

  return (
    <div className='flex flex-col gap-2'>
      <div className='flex flex-wrap items-center gap-2 text-xs'>
        <Tag color='white' shape='circle'>
          {contentType || t('未知')}
        </Tag>
        <Tag color='white' shape='circle'>
          {formatBytes(size)}
        </Tag>
        {omittedReason && (
          <Tag color='orange' shape='circle'>
            {omittedReason}
          </Tag>
        )}
        <Button
          type='tertiary'
          size='small'
          icon={<Copy size={14} />}
          onClick={handleCopy}
        >
          {t('复制')}
        </Button>
      </div>
      <pre
        aria-label={title}
        className='max-h-[55vh] overflow-auto whitespace-pre-wrap break-words rounded-lg border p-3 text-xs leading-relaxed'
        style={{
          borderColor: 'var(--semi-color-border)',
          background: 'var(--semi-color-fill-0)',
        }}
      >
        {displayText}
      </pre>
    </div>
  );
};

const RequestData = () => {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [formApi, setFormApi] = useState(null);
  const [logs, setLogs] = useState([]);
  const [loading, setLoading] = useState(false);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useState(PAGE_SIZE);
  const [total, setTotal] = useState(0);
  const [detailVisible, setDetailVisible] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [selectedLog, setSelectedLog] = useState(null);
  const [detail, setDetail] = useState(null);

  const formInitValues = {
    dateRange: [],
    token_name: '',
    model_name: '',
    username: '',
  };

  const getFilterParams = useCallback(() => {
    const values = formApi ? formApi.getValues() : {};
    const params = {};
    const dateRange = Array.isArray(values.dateRange) ? values.dateRange : [];
    const [start, end] = dateRange;
    const startTimestamp = toUnixTimestamp(start);
    const endTimestamp = toUnixTimestamp(end);

    if (startTimestamp) params.start_timestamp = startTimestamp;
    if (endTimestamp) params.end_timestamp = endTimestamp;
    if (String(values.token_name || '').trim()) {
      params.token_name = String(values.token_name).trim();
    }
    if (String(values.model_name || '').trim()) {
      params.model_name = String(values.model_name).trim();
    }
    if (String(values.username || '').trim()) {
      params.username = String(values.username).trim();
    }

    return params;
  }, [formApi]);

  const loadLogs = useCallback(
    async (page = activePage, size = pageSize) => {
      setLoading(true);
      try {
        const params = {
          p: page,
          page_size: size,
          ...getFilterParams(),
        };
        const res = await API.get('/api/request-log', { params });
        const { success, message, data } = res.data;
        if (success) {
          setLogs(
            (data?.items || []).map((item) => ({
              ...item,
              key: item.id,
            })),
          );
          setActivePage(data?.page || page);
          setPageSize(data?.page_size || size);
          setTotal(data?.total || 0);
        } else {
          showError(message || t('加载请求数据失败'));
        }
      } catch (error) {
        showError(error.message || t('加载请求数据失败'));
      } finally {
        setLoading(false);
      }
    },
    [activePage, getFilterParams, pageSize, t],
  );

  const openDetail = useCallback(
    async (log) => {
      setSelectedLog(log);
      setDetail(log);
      setDetailVisible(true);
      setDetailLoading(true);
      try {
        const res = await API.get(`/api/request-log/${log.id}`);
        const { success, message, data } = res.data;
        if (success) {
          setDetail(data || log);
        } else {
          showError(message || t('加载详情失败'));
        }
      } catch (error) {
        showError(error.message || t('加载详情失败'));
      } finally {
        setDetailLoading(false);
      }
    },
    [t],
  );

  const handleReset = () => {
    if (formApi) {
      formApi.reset();
    }
    setTimeout(() => loadLogs(1, pageSize), 100);
  };

  const columns = useMemo(
    () => [
      {
        title: t('时间'),
        dataIndex: 'created_at',
        render: (value) => (value ? timestamp2string(value) : '-'),
      },
      {
        title: t('用户'),
        dataIndex: 'username',
        render: (value, record) => value || `#${record.user_id || '-'}`,
      },
      {
        title: t('令牌名称'),
        dataIndex: 'token_name',
        render: (value) => (
          <Typography.Text
            ellipsis={{ showTooltip: true }}
            style={{ maxWidth: 160 }}
          >
            {value || '-'}
          </Typography.Text>
        ),
      },
      {
        title: t('模型名称'),
        dataIndex: 'model_name',
        render: (value) => (
          <Typography.Text
            ellipsis={{ showTooltip: true }}
            style={{ maxWidth: 180 }}
          >
            {value || '-'}
          </Typography.Text>
        ),
      },
      {
        title: t('请求路径'),
        dataIndex: 'request_path',
        render: (value, record) => (
          <Typography.Text
            code
            ellipsis={{ showTooltip: true }}
            style={{ maxWidth: 240 }}
          >
            {record.method || 'POST'} {value || '-'}
          </Typography.Text>
        ),
      },
      {
        title: t('状态码'),
        dataIndex: 'status_code',
        render: (value) => (
          <Tag color={statusColor(value)} shape='circle'>
            {value || '-'}
          </Tag>
        ),
      },
      {
        title: t('正文大小'),
        dataIndex: 'body_size',
        render: (value, record) => (
          <div className='flex flex-col text-xs'>
            <span>
              {t('请求')}: {formatBytes(record.request_size)}
            </span>
            <span>
              {t('响应')}: {formatBytes(record.response_size)}
            </span>
          </div>
        ),
      },
      {
        title: '',
        dataIndex: 'operate',
        fixed: 'right',
        render: (value, record) => (
          <Space>
            {record.redacted && (
              <Tag color='blue' shape='circle'>
                <ShieldCheck size={12} />
                {t('已脱敏')}
              </Tag>
            )}
            <Button
              type='tertiary'
              size='small'
              icon={<Eye size={14} />}
              onClick={() => openDetail(record)}
            >
              {t('详情')}
            </Button>
          </Space>
        ),
      },
    ],
    [openDetail, t],
  );

  useEffect(() => {
    loadLogs(1, pageSize);
  }, []);

  const metadata = detail?.metadata ? prettyText(detail.metadata) : '';

  return (
    <div className='mt-[60px] px-2'>
      <CardPro
        type='type2'
        statsArea={
          <div className='flex items-center gap-2'>
            <Database size={18} />
            <span className='font-semibold'>{t('请求数据')}</span>
          </div>
        }
        searchArea={
          <Form
            initValues={formInitValues}
            getFormApi={setFormApi}
            onSubmit={() => loadLogs(1, pageSize)}
            allowEmpty={true}
            autoComplete='off'
            layout='vertical'
            trigger='change'
            stopValidateWithError={false}
          >
            <div className='grid grid-cols-1 md:grid-cols-2 lg:grid-cols-5 gap-2'>
              <div className='col-span-1 lg:col-span-2'>
                <Form.DatePicker
                  field='dateRange'
                  className='w-full'
                  type='dateTimeRange'
                  placeholder={[t('开始时间'), t('结束时间')]}
                  showClear
                  pure
                  size='small'
                  presets={DATE_RANGE_PRESETS.map((preset) => ({
                    text: t(preset.text),
                    start: preset.start(),
                    end: preset.end(),
                  }))}
                />
              </div>
              <Form.Input
                field='token_name'
                prefix={<IconSearch />}
                placeholder={t('令牌名称')}
                showClear
                pure
                size='small'
              />
              <Form.Input
                field='model_name'
                prefix={<IconSearch />}
                placeholder={t('模型名称')}
                showClear
                pure
                size='small'
              />
              <Form.Input
                field='username'
                prefix={<IconSearch />}
                placeholder={t('用户名称')}
                showClear
                pure
                size='small'
              />
            </div>
            <div className='flex justify-end gap-2 mt-2'>
              <Button
                type='tertiary'
                htmlType='submit'
                loading={loading}
                size='small'
                icon={<IconSearch />}
              >
                {t('查询')}
              </Button>
              <Button type='tertiary' onClick={handleReset} size='small'>
                {t('重置')}
              </Button>
              <Button
                type='tertiary'
                onClick={() => loadLogs(activePage, pageSize)}
                size='small'
                icon={<RefreshCw size={14} />}
              >
                {t('刷新')}
              </Button>
            </div>
          </Form>
        }
        paginationArea={createCardProPagination({
          currentPage: activePage,
          pageSize,
          total,
          onPageChange: (page) => loadLogs(page, pageSize),
          onPageSizeChange: (size) => loadLogs(1, size),
          isMobile,
          t,
        })}
        t={t}
      >
        <CardTable
          columns={columns}
          dataSource={logs}
          rowKey='id'
          scroll={{ x: 'max-content' }}
          hidePagination={true}
          loading={loading}
          empty={<Empty description={t('未找到请求数据')} />}
        />
      </CardPro>

      <Modal
        title={
          <div className='flex items-center gap-2'>
            <Database size={16} />
            {t('请求数据详情')}
          </div>
        }
        visible={detailVisible}
        onCancel={() => setDetailVisible(false)}
        footer={null}
        width={isMobile ? '100%' : 960}
      >
        {detailLoading && !detail ? (
          <div className='py-10 text-center'>{t('加载中')}</div>
        ) : (
          <div className='flex flex-col gap-3'>
            <div className='grid grid-cols-1 md:grid-cols-4 gap-2 text-xs'>
              <Tag color='white' shape='circle'>
                {detail?.token_name || selectedLog?.token_name || '-'}
              </Tag>
              <Tag color='white' shape='circle'>
                {detail?.model_name || selectedLog?.model_name || '-'}
              </Tag>
              <Tag color={statusColor(detail?.status_code)} shape='circle'>
                {detail?.status_code || '-'}
              </Tag>
              <Tag color='white' shape='circle'>
                {detail?.request_id || '-'}
              </Tag>
            </div>
            <Tabs type='line' size='small'>
              <TabPane tab={t('请求内容')} itemKey='request'>
                <BodyPanel
                  title={t('请求内容')}
                  contentType={detail?.request_content_type}
                  size={detail?.request_size}
                  omittedReason={detail?.request_omitted_reason}
                  body={detail?.request_body}
                  t={t}
                />
              </TabPane>
              <TabPane tab={t('响应内容')} itemKey='response'>
                <BodyPanel
                  title={t('响应内容')}
                  contentType={detail?.response_content_type}
                  size={detail?.response_size}
                  omittedReason={detail?.response_omitted_reason}
                  body={detail?.response_body}
                  t={t}
                />
              </TabPane>
              <TabPane tab={t('元数据')} itemKey='metadata'>
                <BodyPanel
                  title={t('元数据')}
                  contentType='application/json'
                  size={metadata.length}
                  body={metadata}
                  t={t}
                />
              </TabPane>
            </Tabs>
          </div>
        )}
      </Modal>
    </div>
  );
};

export default RequestData;
