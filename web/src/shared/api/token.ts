/**
 * The access token, held in memory for the life of the tab.
 *
 * Not localStorage and not a readable cookie. Both are reachable by any script
 * the page ever runs, so an XSS bug in a single dependency becomes a stolen
 * credential - and a stored one outlives the tab, so the theft outlives the
 * session. The refresh token is an HttpOnly cookie that script cannot read at
 * all, and a reload recovers the session from that instead.
 *
 * It lives here rather than in the session store because the API client needs
 * it on every request and must not depend on a layer above it. What the token
 * means to the user is the store's business; holding it is transport's.
 */
let accessToken: string | null = null;

const signedOutHandlers = new Set<() => void>();

export const tokenStore = {
  get: () => accessToken,
  set: (value: string | null) => {
    accessToken = value;
  },
  /** Called when a refresh fails, i.e. the session is over. */
  onSignedOut(handler: () => void): () => void {
    signedOutHandlers.add(handler);
    return () => signedOutHandlers.delete(handler);
  },
  announceSignedOut() {
    for (const handler of signedOutHandlers) handler();
  },
};
