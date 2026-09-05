import { create } from "zustand";

import { api, decode, tokenStore } from "@/shared/api";

import { profileSchema, type Profile } from "./schema";

export type SessionStatus = "loading" | "signed-out" | "signed-in";

interface SessionState {
  status: SessionStatus;
  profile: Profile | null;

  /** Reads /me and moves the session to signed-in. */
  loadProfile: () => Promise<void>;
  /** Drops the token and the profile. Does not call the server. */
  clear: () => void;
}

/**
 * The session, in a store rather than a context.
 *
 * A context re-renders every consumer whenever any part of its value changes,
 * and the value here is an object rebuilt on each render - so a routine token
 * rotation used to re-run every effect that depended on it. Components select
 * the one field they read, and a change to another field does not reach them.
 */
export const useSessionStore = create<SessionState>((set) => ({
  status: "loading",
  profile: null,

  loadProfile: async () => {
    const profile = decode(profileSchema, await api.get("/me"), "GET /me");
    set({ profile, status: "signed-in" });
  },

  clear: () => {
    tokenStore.set(null);
    set({ profile: null, status: "signed-out" });
  },
}));

// A refresh that fails means the session ended somewhere far from a component -
// mid-request, in the client. The store hears about it the same way it would
// hear about an explicit sign-out.
tokenStore.onSignedOut(() => {
  useSessionStore.setState({ profile: null, status: "signed-out" });
});

/**
 * Recovers a session from the refresh cookie, once per page load.
 *
 * The promise is memoised at module scope, which is what makes it safe to call
 * from an effect: React's StrictMode invokes a mount effect twice, and two
 * independent refreshes would rotate the token against itself. The second looks
 * like a replay, the server revokes the whole rotation family, and the session
 * dies on every reload. The client also collapses concurrent refreshes, so this
 * is belt and braces - but the belt is what stops a second /me firing too.
 */
let bootstrap: Promise<void> | null = null;

export function restoreSession(): Promise<void> {
  bootstrap ??= (async () => {
    try {
      const restored = await api.restoreSession();
      if (!restored) {
        useSessionStore.setState({ status: "signed-out" });
        return;
      }
      await useSessionStore.getState().loadProfile();
    } catch {
      useSessionStore.setState({ profile: null, status: "signed-out" });
    }
  })();

  return bootstrap;
}

/** Forgets the memoised bootstrap, so a fresh sign-in can restore again. */
export function resetBootstrap() {
  bootstrap = null;
}
