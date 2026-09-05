import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, ApiError, decode, tokenStore } from "@/shared/api";
import { resetBootstrap, sessionSchema, useSessionStore } from "@/entities/session";

export interface Credentials {
  email: string;
  password: string;
  /** Only sent when the server has asked which organisation. */
  organisation?: string;
}

export interface RegisterInput {
  email: string;
  password: string;
  fullName: string;
  organisationName: string;
  organisationSlug: string;
  currency: string;
}

export function useSignIn() {
  const loadProfile = useSessionStore((s) => s.loadProfile);

  return useMutation({
    mutationFn: async ({ email, password, organisation }: Credentials) => {
      const session = decode(
        sessionSchema,
        await api.post("/auth/login", {
          email,
          password,
          ...(organisation ? { organisation_slug: organisation } : {}),
        }),
        "POST /auth/login",
      );
      tokenStore.set(session.access_token);
      await loadProfile();
    },
  });
}

export function useRegister() {
  const loadProfile = useSessionStore((s) => s.loadProfile);

  return useMutation({
    mutationFn: async (input: RegisterInput) => {
      const session = decode(
        sessionSchema,
        await api.post("/auth/register", {
          email: input.email,
          password: input.password,
          full_name: input.fullName,
          organisation_name: input.organisationName,
          organisation_slug: input.organisationSlug,
          currency: input.currency,
        }),
        "POST /auth/register",
      );
      tokenStore.set(session.access_token);
      await loadProfile();
    },
  });
}

export function useSignOut() {
  const clear = useSessionStore((s) => s.clear);
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => {
      try {
        await api.post("/auth/logout");
      } catch (err) {
        // Logout clears a cookie. If the request failed the cookie may survive,
        // but the in-memory token is gone either way, so the session is over
        // from this tab's point of view and holding the user here helps nobody.
        if (!(err instanceof ApiError)) throw err;
      }
    },
    onSettled: () => {
      clear();
      resetBootstrap();
      // Every cached response belongs to the tenant that has just been left.
      // Not clearing it is how the next person to sign in on a shared machine
      // sees the previous one's claims for a frame.
      queryClient.clear();
    },
  });
}
