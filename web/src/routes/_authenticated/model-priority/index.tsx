import { createFileRoute, redirect } from '@tanstack/react-router'

import { hasPermission } from '@/lib/admin-permissions'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/model-priority/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user ||
      auth.user.role < ROLE.ADMIN ||
      !hasPermission(auth.user, 'channel', 'read')) {
      throw redirect({ to: '/403' })
    }
    throw redirect({ to: '/admin/model-priority', replace: true })
  },
})
