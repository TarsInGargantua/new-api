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
import { useState } from 'react'
import { z } from 'zod'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import { api, getSelf } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export function ResetBalanceButton() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const [open, setOpen] = useState(false)
  const queryClient = useQueryClient()
  const schema = z.object({
    username: z.string().refine((value) => !!user && value === user.username, {
      message: t('Username does not match'),
    }),
  })
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { username: '' },
  })
  const mutation = useMutation({
    mutationFn: async (values: z.infer<typeof schema>) => {
      const response = await api.post<{
        success: boolean
        message?: string
        data: { quota: number }
      }>('/api/user/self/reset_balance', values)
      if (!response.data.success) {
        throw new Error(response.data.message || t('Failed to reset balance'))
      }
      return response.data.data
    },
    onSuccess: async (data) => {
      const auth = useAuthStore.getState().auth
      if (auth.user) auth.setUser({ ...auth.user, quota: data.quota })
      setOpen(false)
      form.reset()
      toast.success(t('Balance reset successfully'))
      await queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      // Refresh counters too; a refresh failure must not turn a completed reset
      // into a failed mutation that invites another submission.
      try {
        const result = await getSelf()
        if (result.success) useAuthStore.getState().auth.setUser(result.data)
      } catch (error) {
        handleServerError(error)
      }
    },
    onError: handleServerError,
  })

  const excluded = user?.username.toLowerCase() === 'admin'
  return (
    <>
      <Button
        disabled={!user || excluded}
        title={
          excluded
            ? t('The admin account is excluded from balance resets')
            : undefined
        }
        onClick={() => {
          form.reset()
          setOpen(true)
        }}
      >
        {t('Reset')}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!mutation.isPending) setOpen(value)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('Reset balance')}</DialogTitle>
            <DialogDescription>
              {t(
                'Your balance resets to 100 in the current display unit every day at 00:00 Beijing time. Resetting now replaces your remaining balance with 100 and keeps your usage history.'
              )}
            </DialogDescription>
          </DialogHeader>
          <form
            className='flex flex-col gap-4'
            onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
          >
            <FieldGroup>
              <Field data-invalid={!!form.formState.errors.username}>
                <FieldLabel htmlFor='reset-balance-username'>
                  {t('Enter your username to confirm the reset')}
                </FieldLabel>
                <Input
                  id='reset-balance-username'
                  autoComplete='off'
                  disabled={mutation.isPending}
                  aria-invalid={!!form.formState.errors.username}
                  {...form.register('username')}
                />
                <FieldError errors={[form.formState.errors.username]} />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button
                type='button'
                variant='outline'
                disabled={mutation.isPending}
                onClick={() => setOpen(false)}
              >
                {t('Cancel')}
              </Button>
              <Button
                type='submit'
                disabled={
                  mutation.isPending ||
                  form.watch('username') !== user?.username
                }
              >
                {mutation.isPending ? t('Processing...') : t('Confirm reset')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
