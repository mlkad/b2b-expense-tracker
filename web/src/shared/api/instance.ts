import { ApiClient } from "./client";
import { tokenStore } from "./token";

/**
 * One client for the whole app.
 *
 * A single instance is what makes the single-flight refresh work: it collapses
 * concurrent 401s onto one request, and it can only do that for callers that
 * share it. A client per component would send one refresh per panel, and a
 * rotated token replayed is treated as theft - so the user would be signed out
 * for opening a page with six panels on it.
 */
export const api = new ApiClient({
  getToken: tokenStore.get,
  setToken: tokenStore.set,
  onSignedOut: tokenStore.announceSignedOut,
});
