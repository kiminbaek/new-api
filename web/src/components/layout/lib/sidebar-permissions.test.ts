import { describe, expect, it } from 'vitest'

import type { NavItem } from '@/components/layout/types'
import { ROLE } from '@/lib/roles'
import type { AuthUser } from '@/stores/auth-store'

import { canViewSidebarItem } from './sidebar-permissions'

const item: NavItem = {
  title: 'Channels',
  url: '/channels',
  requiredRole: ROLE.ADMIN,
  requiredPermission: { resource: 'channel', action: 'read' },
}

function user(role: number, read?: boolean): AuthUser {
  return {
    id: role,
    username: `role-${role}`,
    role,
    permissions: {
      admin_permissions: { channel: { read: read === true } },
    },
  }
}

describe('canViewSidebarItem', () => {
  it('allows super administrators without an explicit matrix grant', () => {
    expect(canViewSidebarItem(item, user(ROLE.SUPER_ADMIN))).toBe(true)
  })

  it('allows an administrator with channel read permission', () => {
    expect(canViewSidebarItem(item, user(ROLE.ADMIN, true))).toBe(true)
  })

  it('hides the item when channel read permission is revoked', () => {
    expect(canViewSidebarItem(item, user(ROLE.ADMIN, false))).toBe(false)
  })

  it('rejects users below the required role', () => {
    expect(canViewSidebarItem(item, user(ROLE.USER, true))).toBe(false)
  })
})
