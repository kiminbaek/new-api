export interface SuccessfulApiResponse {
  success: boolean
  message?: string
}

export function requireSuccessfulResponse<T extends SuccessfulApiResponse>(
  response: T,
  fallbackMessage: string
): T {
  if (!response.success) {
    throw new Error(response.message || fallbackMessage)
  }
  return response
}
