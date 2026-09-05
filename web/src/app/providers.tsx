import { useEffect, useState, type ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { NuqsAdapter } from "nuqs/adapters/react-router/v7";

import { restoreSession } from "@/entities/session";

import { createQueryClient } from "./query-client";

export function Providers({ children }: { children: ReactNode }) {
  // Created in state rather than at module scope: a client per mount is what
  // keeps a test's cache from leaking into the next test, and there is only
  // ever one mount in the browser.
  const [queryClient] = useState(createQueryClient);

  // The session is recovered from the refresh cookie once per page load. The
  // promise behind this is memoised, so StrictMode invoking the effect twice
  // does not send two refreshes - which would rotate the token against itself
  // and revoke the family.
  useEffect(() => {
    void restoreSession();
  }, []);

  return (
    <QueryClientProvider client={queryClient}>
      {/* nuqs reads and writes the query string through the router, so that a
          filter change is a navigation the router knows about rather than a
          history entry it will later disagree with. */}
      <NuqsAdapter>{children}</NuqsAdapter>
    </QueryClientProvider>
  );
}
