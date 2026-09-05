import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { ApiClient, ApiError } from "../api/client";
import type { Profile, Session } from "../api/types";
import { SessionContext, type RegisterInput, type SessionState } from "./context";

/**
 * Holds the access token, in memory, for the life of the tab.
 *
 * Not localStorage and not a readable cookie. Both are reachable by any script
 * the page ever runs, so an XSS bug in a single dependency becomes a stolen
 * credential - and a stored one outlives the tab, so the theft outlives the
 * session. The refresh token is an HttpOnly cookie that script cannot read at
 * all, and a reload recovers the session from that instead.
 *
 * A ref, not state. The token changes on every rotation, and state would
 * re-create the API client and re-run every effect that depends on it - so a
 * routine refresh would reload the whole dashboard. Nothing renders this
 * value, so nothing needs to know when it changes.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const tokens = useRef<string | null>(null);

  const [status, setStatus] = useState<SessionState["status"]>("loading");
  const [profile, setProfile] = useState<Profile | null>(null);

  const handleSignedOut = useCallback(() => {
    tokens.current = null;
    setProfile(null);
    setStatus("signed-out");
  }, []);

  const api = useMemo(
    () =>
      new ApiClient({
        getToken: () => tokens.current,
        setToken: (value) => {
          tokens.current = value;
        },
        onSignedOut: handleSignedOut,
      }),
    [handleSignedOut],
  );

  const loadProfile = useCallback(async () => {
    const me = await api.get<Profile>("/me");
    setProfile(me);
    setStatus("signed-in");
  }, [api]);

  /**
   * On load, try to recover a session from the refresh cookie.
   *
   * This is what makes an in-memory access token workable: a reload has no
   * token, asks for one, and gets it if the browser still holds the cookie.
   * A 401 here is the ordinary case for a first visit, not an error.
   */
  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        // restoreSession, not a direct POST. StrictMode runs this effect twice
        // and two independent refreshes would rotate the token against itself:
        // the second looks like a replayed token, the server revokes the whole
        // family, and the session dies on every reload. The client collapses
        // both calls onto one request.
        const restored = await api.restoreSession();
        if (cancelled) return;
        if (restored) await loadProfile();
        else handleSignedOut();
      } catch {
        if (!cancelled) handleSignedOut();
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [api, loadProfile, handleSignedOut]);

  const signIn = useCallback(
    async (email: string, password: string, organisation?: string) => {
      const session = await api.post<Session>("/auth/login", {
        email,
        password,
        ...(organisation ? { organisation_slug: organisation } : {}),
      });
      tokens.current = session.access_token;
      await loadProfile();
    },
    [api, loadProfile],
  );

  const register = useCallback(
    async (input: RegisterInput) => {
      const session = await api.post<Session>("/auth/register", {
        email: input.email,
        password: input.password,
        full_name: input.fullName,
        organisation_name: input.organisationName,
        organisation_slug: input.organisationSlug,
        currency: input.currency,
      });
      tokens.current = session.access_token;
      await loadProfile();
    },
    [api, loadProfile],
  );

  const signOut = useCallback(async () => {
    try {
      await api.post("/auth/logout");
    } catch (err) {
      // Logout clears a cookie. If the request failed the cookie may survive,
      // but the in-memory token is gone either way, so the session is over
      // from this tab's point of view and holding the user here helps nobody.
      if (!(err instanceof ApiError)) throw err;
    } finally {
      handleSignedOut();
    }
  }, [api, handleSignedOut]);

  const can = useCallback(
    (permission: string) => profile?.permissions.includes(permission) ?? false,
    [profile],
  );

  const value = useMemo<SessionState>(
    () => ({ status, profile, api, signIn, register, signOut, reloadProfile: loadProfile, can }),
    [status, profile, api, signIn, register, signOut, loadProfile, can],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}
