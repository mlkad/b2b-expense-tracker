import { useSessionStore } from "./store";

import type { Profile } from "./schema";

export const useProfile = (): Profile | null => useSessionStore((s) => s.profile);
export const useSessionStatus = () => useSessionStore((s) => s.status);

/**
 * Whether this membership holds a permission.
 *
 * The list comes from the server with the profile. The dashboard uses it to
 * decide what to *show*; the server decides what is allowed. Hiding a button
 * the API would refuse is a courtesy, not a control.
 */
export function useCan(permission: string): boolean {
  return useSessionStore((s) => s.profile?.permissions.includes(permission) ?? false);
}
