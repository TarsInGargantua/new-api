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
import React, { useContext, useState } from 'react';
import { Button, Input, Modal } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';
import { UserContext } from '../../context/User';
import { API, showError, showSuccess } from '../../helpers';

const ResetBalanceButton = () => {
  const { t } = useTranslation();
  const [userState, userDispatch] = useContext(UserContext);
  const [open, setOpen] = useState(false);
  const [username, setUsername] = useState('');
  const [pending, setPending] = useState(false);
  const user = userState?.user;
  const excluded = !user || ![1, 10, 100].includes(user.role);

  const resetBalance = async () => {
    if (pending || !user || username !== user.username) return;
    setPending(true);
    try {
      const response = await API.post('/api/user/self/reset_balance', {
        username,
      });
      if (!response.data.success) {
        showError(response.data.message || t('余额重置失败'));
        return;
      }
      const updated = { ...user, quota: response.data.data.quota };
      userDispatch({ type: 'login', payload: updated });
      localStorage.setItem('user', JSON.stringify(updated));
      setOpen(false);
      setUsername('');
      showSuccess(t('余额已重置'));
    } catch (error) {
      showError(error.response?.data?.message || t('余额重置失败'));
    } finally {
      setPending(false);
    }
  };

  return (
    <>
      <Button
        theme='borderless'
        size='small'
        disabled={!user || excluded}
        title={excluded ? t('当前角色不参与余额重置') : undefined}
        onClick={(event) => {
          event.stopPropagation();
          setUsername('');
          setOpen(true);
        }}
      >
        {t('重置')}
      </Button>
      <Modal
        title={t('重置余额')}
        width='min(520px, calc(100vw - 32px))'
        visible={open}
        onCancel={() => {
          if (!pending) setOpen(false);
        }}
        onOk={resetBalance}
        okText={t('确认重置')}
        cancelText={t('取消')}
        confirmLoading={pending}
        okButtonProps={{
          disabled: pending || !username || username !== user?.username,
        }}
        cancelButtonProps={{ disabled: pending }}
        closeOnEsc={!pending}
        maskClosable={!pending}
      >
        <label htmlFor='reset-balance-username'>
          {t('请输入自己的用户名以确认重置')}
        </label>
        <Input
          id='reset-balance-username'
          value={username}
          onChange={setUsername}
          disabled={pending}
          autoComplete='off'
          onEnterPress={resetBalance}
        />
      </Modal>
    </>
  );
};

export default ResetBalanceButton;
