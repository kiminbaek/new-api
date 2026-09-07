import { useMutation, useQueryClient } from '@tanstack/react-query'

import { resetModelRatios, updatePricingOptionsBulk } from '../api'

function usePricingCacheInvalidation() {
  const queryClient = useQueryClient()
  return async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['pricing'] }),
      queryClient.invalidateQueries({ queryKey: ['system-options'] }),
    ])
  }
}

export function usePricingOptionsMutation() {
  const invalidatePricing = usePricingCacheInvalidation()
  return useMutation({
    mutationFn: updatePricingOptionsBulk,
    onSuccess: invalidatePricing,
  })
}

export function useResetModelRatiosMutation() {
  const invalidatePricing = usePricingCacheInvalidation()
  return useMutation({
    mutationFn: resetModelRatios,
    onSuccess: invalidatePricing,
  })
}
