import { QueryClient } from "@tanstack/react-query";

import { ApiError, ContractError } from "@/shared/api";

/**
 * Retries are for a network that failed, not for a server that answered.
 *
 * A 403 retried three times is three 403s and a slower error message; a 409
 * retried is a decision the user has not been told about yet. Only a 5xx or a
 * request that never arrived is worth attempting again - and a response that
 * did not match its schema will not match it on a second read either.
 */
function shouldRetry(failureCount: number, error: Error): boolean {
  if (error instanceof ContractError) return false;
  if (error instanceof ApiError && error.status < 500) return false;
  return failureCount < 2;
}

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: shouldRetry,
        // Long enough that moving between screens does not refetch what was
        // just read, short enough that a decision made in another tab shows up
        // without a reload. Anything that must be current after a write is
        // invalidated explicitly by the mutation that caused it.
        staleTime: 30 * 1000,
        // The window regaining focus is not new information about expenses.
        refetchOnWindowFocus: false,
      },
      mutations: {
        retry: false,
      },
    },
  });
}
