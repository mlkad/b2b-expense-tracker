import { Route, Routes } from "react-router";

import { useSessionStatus } from "@/entities/session";
import { ApprovalsPage } from "@/pages/approvals";
import { BudgetsPage } from "@/pages/budgets";
import { ExpenseDetailPage } from "@/pages/expense-detail";
import { ExpenseFormPage } from "@/pages/expense-form";
import { ExpensesPage } from "@/pages/expenses";
import { OrganisationPage } from "@/pages/organisation";
import { OverviewPage } from "@/pages/overview";
import { SignInPage } from "@/pages/sign-in";
import { AppShell } from "@/widgets/app-shell";

/**
 * Routes are chosen by session state rather than guarded per route.
 *
 * A signed-out visitor sees exactly one screen, so there is no protected route
 * to forget to guard - the authenticated tree is not mounted at all. The server
 * enforces everything regardless; this is about not rendering a page that would
 * only fill with 401s.
 */
export function Router() {
  const status = useSessionStatus();

  if (status === "loading") {
    return (
      // Deliberately plain. This is on screen for one request while the session
      // is recovered from the refresh cookie, and a skeleton of a layout the
      // visitor may not be entitled to see would flash and vanish.
      <div className="grid min-h-dvh place-items-center text-sm text-ink-600" role="status">
        Loading…
      </div>
    );
  }

  if (status === "signed-out") return <SignInPage />;

  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<OverviewPage />} />
        <Route path="expenses" element={<ExpensesPage />} />
        {/* "new" before ":id", or the literal would be read as an id. */}
        <Route path="expenses/new" element={<ExpenseFormPage />} />
        <Route path="expenses/:id" element={<ExpenseDetailPage />} />
        <Route path="expenses/:id/edit" element={<ExpenseFormPage />} />
        <Route path="approvals" element={<ApprovalsPage />} />
        <Route path="budgets" element={<BudgetsPage />} />
        <Route path="organisation" element={<OrganisationPage />} />
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
