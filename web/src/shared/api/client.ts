import type { ErrorBody, FieldError } from "./errors";

/**
 * ApiError carries what the server said, in the shape a form can use.
 *
 * The server returns one error envelope for every failure, so there is one
 * class here rather than a union - and `fields` is what lets a message appear
 * next to the input that caused it instead of as a banner saying something
 * went wrong.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly fields: FieldError[];
  readonly traceId?: string;

  constructor(status: number, message: string, fields: FieldError[] = [], traceId?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.fields = fields;
    this.traceId = traceId;
  }

  /** The message for one field, if the server rejected it. */
  fieldError(name: string): string | undefined {
    return this.fields.find((f) => f.field === name)?.detail;
  }

  /** A plan ceiling rather than a permission problem: offer the upgrade. */
  get isPlanLimit(): boolean {
    return this.status === 402;
  }

  /** The world moved. Reload and decide again. */
  get isConflict(): boolean {
    return this.status === 409;
  }
}

/** Thrown when the session cannot be recovered. The app signs out. */
export class SessionExpired extends Error {
  constructor() {
    super("session expired");
    this.name = "SessionExpired";
  }
}

type TokenSource = () => string | null;
type TokenSink = (token: string | null) => void;

export interface ClientOptions {
  baseUrl?: string;
  getToken: TokenSource;
  setToken: TokenSink;
  onSignedOut: () => void;
}

/**
 * ApiClient wraps fetch with the two things every call needs: the bearer token,
 * and a single-flight refresh when it has expired.
 */
export class ApiClient {
  private readonly baseUrl: string;
  private readonly getToken: TokenSource;
  private readonly setToken: TokenSink;
  private readonly onSignedOut: () => void;

  /**
   * The in-flight refresh, shared by every caller that hits a 401 at once.
   *
   * Without this, a dashboard that loads six panels in parallel sends six
   * refreshes the moment the token expires. Refresh tokens rotate and a reused
   * one is treated as theft - so five of those six would be rejected, the
   * whole rotation family revoked, and the user signed out for doing nothing
   * but opening a page.
   */
  private refreshing: Promise<string> | null = null;

  constructor(opts: ClientOptions) {
    this.baseUrl = opts.baseUrl ?? "/api/v1";
    this.getToken = opts.getToken;
    this.setToken = opts.setToken;
    this.onSignedOut = opts.onSignedOut;
  }

  get<T>(path: string, init?: RequestInit): Promise<T> {
    return this.request<T>("GET", path, undefined, init);
  }

  post<T>(path: string, body?: unknown, init?: RequestInit): Promise<T> {
    return this.request<T>("POST", path, body, init);
  }

  patch<T>(path: string, body?: unknown, init?: RequestInit): Promise<T> {
    return this.request<T>("PATCH", path, body, init);
  }

  delete<T>(path: string, init?: RequestInit): Promise<T> {
    return this.request<T>("DELETE", path, undefined, init);
  }

  /** The absolute URL for a path, used for downloads the browser navigates to. */
  url(path: string): string {
    return `${this.baseUrl}${path}`;
  }

  /**
   * Recovers a session from the refresh cookie, on load.
   *
   * It goes through the same single-flight path as an expiry-triggered
   * refresh, and that is not tidiness - it is the fix for a real bug. An
   * earlier version called POST /auth/refresh directly here, and React's
   * StrictMode invokes a mount effect twice in development, so two refreshes
   * left at the same millisecond. Refresh tokens rotate and a reused one is
   * treated as theft, so the second was rejected, the rotation family revoked,
   * and the session died on every reload:
   *
   *   POST /api/v1/auth/refresh 401  (00:41:15.466)
   *   POST /api/v1/auth/refresh 401  (00:41:15.466)
   *
   * StrictMode only made it reproducible. Two tabs opening together would do
   * the same thing in production.
   */
  async restoreSession(): Promise<boolean> {
    const token = await this.refreshOnce({ signOutOnFailure: false });
    return token !== null;
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    init?: RequestInit,
  ): Promise<T> {
    let response = await this.send(method, path, body, init);

    // One retry, and only for an expired token. Retrying anything else would
    // repeat a write the server may already have applied.
    if (response.status === 401 && this.getToken() !== null) {
      const token = await this.refreshOnce({ signOutOnFailure: true });
      if (token === null) throw new SessionExpired();
      response = await this.send(method, path, body, init);
    }

    return this.decode<T>(response);
  }

  private async send(
    method: string,
    path: string,
    body?: unknown,
    init?: RequestInit,
  ): Promise<Response> {
    const headers = new Headers(init?.headers);
    headers.set("Accept", "application/json");

    const token = this.getToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    if (body !== undefined) headers.set("Content-Type", "application/json");

    return fetch(`${this.baseUrl}${path}`, {
      ...init,
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      // The refresh token is an HttpOnly cookie, so it only travels if the
      // request is told to send credentials.
      credentials: "same-origin",
    });
  }

  /**
   * Rotates the session, collapsing concurrent callers onto one request.
   *
   * signOutOnFailure separates the two callers. A refresh that fails after a
   * 401 means a live session just ended and the user should be told; one that
   * fails on load means there was no session to recover, which is the ordinary
   * state of a first visit and not something to announce.
   */
  private refreshOnce({ signOutOnFailure }: { signOutOnFailure: boolean }): Promise<string | null> {
    if (!this.refreshing) {
      this.refreshing = fetch(`${this.baseUrl}/auth/refresh`, {
        method: "POST",
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      })
        .then(async (res) => {
          if (!res.ok) throw new SessionExpired();
          const session = (await res.json()) as { access_token: string };
          this.setToken(session.access_token);
          return session.access_token;
        })
        .finally(() => {
          // Cleared whichever way it went, so the next expiry starts a fresh
          // attempt rather than resolving against a stale promise.
          this.refreshing = null;
        });
    }

    return this.refreshing.catch(() => {
      this.setToken(null);
      if (signOutOnFailure) this.onSignedOut();
      return null;
    });
  }

  private async decode<T>(response: Response): Promise<T> {
    if (response.status === 204) return undefined as T;

    const text = await response.text();

    if (!response.ok) {
      let body: ErrorBody | undefined;
      try {
        body = text ? (JSON.parse(text) as ErrorBody) : undefined;
      } catch {
        // A non-JSON error body means something in front of the API answered -
        // a proxy timeout page, usually. The status is still the useful part.
      }
      throw new ApiError(
        response.status,
        body?.message ?? `request failed with status ${response.status}`,
        body?.fields ?? [],
        body?.trace_id,
      );
    }

    return text ? (JSON.parse(text) as T) : (undefined as T);
  }
}
