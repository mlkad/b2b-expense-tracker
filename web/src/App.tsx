import { BrowserRouter, Route, Routes } from "react-router";

import { Layout } from "./components/Layout";
import { Approvals } from "./routes/Approvals";
import { ExpenseDetail } from "./routes/ExpenseDetail";
import { ExpenseForm } from "./routes/ExpenseForm";
import { Expenses } from "./routes/Expenses";
import { Overview } from "./routes/Overview";
import { SignIn } from "./routes/SignIn";
import { useSession } from "./auth/context";
import { SessionProvider } from "./auth/session";

/**
 * Routes are chosen by session state rather than guarded per route.
 *
 * A signed-out visitor sees exactly one screen, so there is no protected route
 * to forget to guard - the authenticated tree is not mounted at all. The
 * server enforces everything regardless; this is about not rendering a page
 * that would only fill with 401s.
 */
function Router() {
  const { status } = useSession();

  if (status === "loading") {
    return (
      // Deliberately plain. This is on screen for one request while the
      // session is recovered from the refresh cookie, and a skeleton of a
      // layout the visitor may not be entitled to see would flash and vanish.
      <div className="grid min-h-dvh place-items-center text-sm text-ink-600" role="status">
        Loading…
      </div>
    );
  }

  if (status === "signed-out") return <SignIn />;

  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Overview />} />
        <Route path="expenses" element={<Expenses />} />
        {/* "new" before ":id", or the literal would be read as an id. */}
        <Route path="expenses/new" element={<ExpenseForm />} />
        <Route path="expenses/:id" element={<ExpenseDetail />} />
        <Route path="expenses/:id/edit" element={<ExpenseForm />} />
        <Route path="approvals" element={<Approvals />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

function NotFound() {
  return (
    <div className="py-12 text-center">
      <p className="text-sm font-medium">That page does not exist.</p>
    </div>
  );
}

export function App() {
  return (
    <BrowserRouter>
      <SessionProvider>
        <Router />
      </SessionProvider>
    </BrowserRouter>
  );
}
