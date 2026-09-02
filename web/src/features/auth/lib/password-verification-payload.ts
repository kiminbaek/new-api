export interface PasswordEncryptionCapability {
  enabled?: boolean
  kid?: string
  public_key?: string
}

export type PasswordVerificationPayload =
  | { password: string }
  | { password_encrypted: string; encryption_key_id: string }

export async function buildPasswordVerificationPayload(
  password: string,
  capability: PasswordEncryptionCapability,
  encrypt: (password: string, publicKey: string) => Promise<string>
): Promise<PasswordVerificationPayload> {
  if (capability.enabled === false) {
    return { password }
  }
  if (!capability.kid || !capability.public_key) {
    throw new Error('Password encryption capability is incomplete')
  }
  return {
    password_encrypted: await encrypt(password, capability.public_key),
    encryption_key_id: capability.kid,
  }
}
