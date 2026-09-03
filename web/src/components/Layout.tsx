import { NavLink, Outlet } from "react-router";

import { Badge, Button } from "./ui";
import { useSession } from "../auth/context";

interface NavItem {
  to: string;
  label: string;
  /** Hidden when the caller lacks it. A convenience, never the enforcement. */
  permission?: string;
}

const NAV: NavItem[] = [
  { to: "/", label: "Overview" },
  { to: "/expenses", label: "Expenses" },
  { to: "/approvals", label: "Approvals", permission: "expense:approve" },
  { to: "/budgets", label: "Budgets", permission: "expense:read:team" },
  { to: "/organisation", label: "Organisation", permission: "member:manage" },
];

export function Layout() {
  const { profile, signOut, can } = useSession();

  return (
    <div className="min-h-dvh">
      {/* A skip link, first in the tab order. Without it, reaching the table
          on a page with a dozen navigation items means a dozen tab presses on
          every navigation. */}
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-white focus:px-4 focus:py-2 focus:shadow"
      >
        Skip to content
      </a>

      <header className="border-b border-ink-100 bg-white">
        <div className="mx-auto flex max-w-6xl items-center gap-6 px-6 py-3">
          <span className="text-sm font-semibold">{profile?.tenant_name ?? "Expenses"}</span>

          <nav aria-label="Main" className="flex items-center gap-1">
            {NAV.filter((item) => !item.permission || can(item.permission)).map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.to === "/"}
                className={({ isActive }) =>
                  `rounded-md px-3 py-1.5 text-sm ${
                    isActive ? "bg-ink-100 font-medium text-ink-900" : "text-ink-600 hover:bg-ink-50"
                  }`
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>

          <div className="ml-auto flex items-center gap-3">
            {profile && (
              <span className="hidden items-center gap-2 text-sm text-ink-600 sm:flex">
                {profile.full_name || profile.email}
                <Badge tone="brand">{profile.role}</Badge>
              </span>
            )}
            <Button variant="ghost" onClick={() => void signOut()}>
              Sign out
            </Button>
          </div>
        </div>
      </header>

      <main id="main" className="mx-auto max-w-6xl px-6 py-8">
        <Outlet />
      </main>
    </div>
  );
}
