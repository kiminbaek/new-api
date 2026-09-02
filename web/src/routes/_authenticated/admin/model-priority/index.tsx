import { createFileRoute, redirect } from '@tanstack/react-router'

import { ModelPriority } from '@/features/model-priority'
import { hasPermission } from '@/lib/admin-permissions'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute(
  '/_authenticated/admin/model-priority/'
)({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user ||
      auth.user.role < ROLE.ADMIN ||
      !hasPermission(auth.user, 'channel', 'read')) {
      throw redirect({ to: '/403' })
    }
  },
  component: ModelPriority,
})
