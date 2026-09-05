import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiClient, ApiError, SessionExpired } from "./client";

/** A fetch stand-in that records what it was asked for. */
function stubFetch(responses: Array<() => Response | Promise<Response>>) {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  let index = 0;

  const impl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    const next = responses[Math.min(index, responses.length - 1)];
    index += 1;
    return next();
  });

  vi.stubGlobal("fetch", impl);
  return { calls, impl };
}

/**
 * Runs a call that is expected to fail and returns the error, typed.
 *
 * `await promise.catch(e => e)` gives `unknown`, and narrowing it at every
 * assertion buries what the test is actually checking.
 */
async function failure(promise: Promise<unknown>): Promise<ApiError> {
  try {
    await promise;
  } catch (err) {
    if (err instanceof ApiError) return err;
    throw err;
  }
  throw new Error("expected the request to fail");
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

function makeClient(overrides: { token?: string | null } = {}) {
  // "token" in overrides rather than ?? - `null ?? "initial-token"` yields the
  // default, so a test asking for a signed-out client would silently get a
  // signed-in one and pass for the wrong reason.
  let token: string | null = "token" in overrides ? (overrides.token ?? null) : "initial-token";
  const signedOut = vi.fn();

  const api = new ApiClient({
    getToken: () => token,
    setToken: (value) => {
      token = value;
    },
    onSignedOut: signedOut,
  });

  return { api, signedOut, currentToken: () => token };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("requests", () => {
  it("sends the bearer token and same-origin credentials", async () => {
    const { calls } = stubFetch([() => json(200, { ok: true })]);
    const { api } = makeClient();

    await api.get("/expenses");

    const headers = new Headers(calls[0].init.headers);
    expect(headers.get("Authorization")).toBe("Bearer initial-token");
    // The refresh token is an HttpOnly cookie and only travels when the
    // request is told to send credentials.
    expect(calls[0].init.credentials).toBe("same-origin");
  });

  it("does not set Content-Type on a body-less request", async () => {
    const { calls } = stubFetch([() => json(200, {})]);
    const { api } = makeClient();

    await api.get("/expenses");

    expect(new Headers(calls[0].init.headers).get("Content-Type")).toBeNull();
  });

  it("returns undefined for 204 rather than failing to parse an empty body", async () => {
    stubFetch([() => new Response(null, { status: 204 })]);
    const { api } = makeClient();

    await expect(api.delete("/expenses/1")).resolves.toBeUndefined();
  });
});

describe("errors", () => {
  it("carries the field errors so a form can place them", async () => {
    stubFetch([
      () =>
        json(422, {
          status: 422,
          message: "the request could not be processed",
          fields: [{ field: "amount_minor", detail: "must be greater than zero" }],
          trace_id: "abc123",
        }),
    ]);
    const { api } = makeClient();

    const err = await failure(api.post("/expenses", {}));

    expect(err.status).toBe(422);
    expect(err.fieldError("amount_minor")).toBe("must be greater than zero");
    expect(err.fieldError("merchant")).toBeUndefined();
    // The reference is what a support conversation needs: it is on every log
    // line for that request.
    expect(err.traceId).toBe("abc123");
  });

  it("distinguishes a plan ceiling from a permission problem", async () => {
    stubFetch([() => json(402, { status: 402, message: "the free plan includes 1 department" })]);
    const { api } = makeClient();

    const err = await failure(api.post("/departments", {}));
    expect(err.isPlanLimit).toBe(true);
    expect(err.isConflict).toBe(false);
  });

  it("survives an error body that is not JSON", async () => {
    // A proxy timeout page, usually. The status is still the useful part.
    stubFetch([() => new Response("<html>504 Gateway Timeout</html>", { status: 504 })]);
    const { api } = makeClient();

    const err = await failure(api.get("/expenses"));
    expect(err.status).toBe(504);
  });
});

describe("token refresh", () => {
  it("refreshes once on 401 and retries the original request", async () => {
    const { calls } = stubFetch([
      () => json(401, { status: 401, message: "invalid or expired token" }),
      () => json(200, { access_token: "fresh-token" }),
      () => json(200, { items: [] }),
    ]);
    const { api, currentToken } = makeClient();

    await expect(api.get("/expenses")).resolves.toEqual({ items: [] });

    expect(calls.map((c) => c.url)).toEqual([
      "/api/v1/expenses",
      "/api/v1/auth/refresh",
      "/api/v1/expenses",
    ]);
    expect(currentToken()).toBe("fresh-token");
    // The retry carries the new token, not the expired one.
    expect(new Headers(calls[2].init.headers).get("Authorization")).toBe("Bearer fresh-token");
  });

  /**
   * The reason the refresh is single-flight.
   *
   * Refresh tokens rotate, and a reused one is treated as theft: the whole
   * rotation family is revoked. A dashboard that loads six panels at once
   * would send six refreshes the moment the token expired, five would be
   * rejected as replays, and the user would be signed out for opening a page.
   */
  it("collapses concurrent 401s onto one refresh", async () => {
    let refreshes = 0;

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);

        if (url.endsWith("/auth/refresh")) {
          refreshes += 1;
          // A real refresh is a round trip; resolving on a later tick is what
          // gives a naive implementation the chance to start a second one.
          await new Promise((resolve) => setTimeout(resolve, 10));
          return json(200, { access_token: "fresh-token" });
        }

        return refreshes === 0
          ? json(401, { status: 401, message: "expired" })
          : json(200, { ok: url });
      }),
    );

    const { api } = makeClient();

    const results = await Promise.all([
      api.get("/expenses"),
      api.get("/budgets"),
      api.get("/summary"),
      api.get("/me"),
      api.get("/departments"),
      api.get("/members"),
    ]);

    expect(refreshes).toBe(1);
    expect(results).toHaveLength(6);
  });

  it("signs out when the refresh itself fails", async () => {
    stubFetch([
      () => json(401, { status: 401, message: "expired" }),
      () => json(401, { status: 401, message: "expired" }),
    ]);
    const { api, signedOut, currentToken } = makeClient();

    await expect(api.get("/expenses")).rejects.toBeInstanceOf(SessionExpired);
    expect(signedOut).toHaveBeenCalledOnce();
    expect(currentToken()).toBeNull();
  });

  it("does not try to refresh when there was no token to begin with", async () => {
    // A first visit. Asking to refresh a session that was never established
    // would send the visitor a 401 for a request they did not make.
    const { calls } = stubFetch([() => json(401, { status: 401, message: "missing bearer token" })]);
    const { api, signedOut } = makeClient({ token: null });

    await expect(api.get("/me")).rejects.toBeInstanceOf(ApiError);
    expect(calls).toHaveLength(1);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("starts a fresh attempt after a failed one rather than reusing the promise", async () => {
    let attempt = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/auth/refresh")) {
          attempt += 1;
          return attempt === 1 ? json(401, {}) : json(200, { access_token: "second-chance" });
        }
        return attempt >= 2 ? json(200, { ok: true }) : json(401, { status: 401, message: "expired" });
      }),
    );

    const first = makeClient();
    await expect(first.api.get("/expenses")).rejects.toBeInstanceOf(SessionExpired);

    // A new client, as a fresh sign-in would produce. The point is that the
    // rejected promise was cleared rather than cached.
    const second = makeClient();
    await expect(second.api.get("/expenses")).resolves.toEqual({ ok: true });
    expect(attempt).toBe(2);
  });
});
