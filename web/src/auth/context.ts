import { createContext, useContext } from "react";

import type { ApiClient } from "../api/client";
import type { Profile } from "../api/types";

export interface RegisterInput {
  email: string;
  password: string;
  fullName: string;
  organisationName: string;
  organisationSlug: string;
  currency: string;
}

export interface SessionState {
  status: "loading" | "signed-out" | "signed-in";
  profile: Profile | null;
  api: ApiClient;
  signIn: (email: string, password: string, organisation?: string) => Promise<void>;
  register: (input: RegisterInput) => Promise<void>;
  signOut: () => Promise<void>;
  reloadProfile: () => Promise<void>;
  can: (permission: string) => boolean;
}

/**
 * The context and its hook live apart from the provider component.
 *
 * Fast Refresh only preserves state for a module that exports components and
 * nothing else. With the hook in the same file as the provider, every edit to
 * either one remounts the tree and signs the developer out - which is a small
 * thing that makes working on the authenticated screens genuinely unpleasant.
 */
export const SessionContext = createContext<SessionState | null>(null);

export function useSession(): SessionState {
  const ctx = useContext(SessionContext);
  if (!ctx) {
    // Reading the session outside the provider is a wiring mistake, and one
    // that would otherwise surface as "cannot read property of null" somewhere
    // unrelated to the component that caused it.
    throw new Error("useSession must be used inside a SessionProvider");
  }
  return ctx;
}
