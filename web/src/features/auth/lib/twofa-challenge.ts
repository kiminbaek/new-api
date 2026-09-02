export type TwoFAChallenge = {
  require_2fa: true
  flow_token: string
  expires_at?: number
}

export function getTwoFAChallenge(value: unknown): TwoFAChallenge | null {
  if (!value || typeof value !== 'object') return null
  const candidate = value as Partial<TwoFAChallenge>
  if (candidate.require_2fa !== true) return null
  if (typeof candidate.flow_token !== 'string' || !candidate.flow_token.trim()) {
    return null
  }
  return {
    require_2fa: true,
    flow_token: candidate.flow_token,
    expires_at:
      typeof candidate.expires_at === 'number'
        ? candidate.expires_at
        : undefined,
  }
}
