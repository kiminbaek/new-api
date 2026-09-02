import type { NavItem } from '@/components/layout/types'
import { hasPermission } from '@/lib/admin-permissions'
import { ROLE } from '@/lib/roles'
import type { AuthUser } from '@/stores/auth-store'

export function canViewSidebarItem(
  item: NavItem,
  user: AuthUser | null | undefined
): boolean {
  const role = user?.role ?? ROLE.GUEST
  if (item.requiredRole !== undefined && role < item.requiredRole) {
    return false
  }
  if (item.requiredPermission) {
    return hasPermission(
      user,
      item.requiredPermission.resource,
      item.requiredPermission.action
    )
  }
  return true
}
