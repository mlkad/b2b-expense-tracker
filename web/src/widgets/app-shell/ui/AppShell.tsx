import { NavLink, Outlet, useLocation } from "react-router";

import { useProfile, useSessionStore } from "@/entities/session";
import {
  ApprovalsIcon,
  BudgetsIcon,
  ExpensesIcon,
  LogoMark,
  OrganisationIcon,
  OverviewIcon,
} from "@/shared/ui/icons";

import { AccountMenu } from "./AccountMenu";
import { GlobalSearch } from "./GlobalSearch";
import { NotificationBell } from "./NotificationBell";
import { RailArt, type Scene } from "./RailArt";

interface NavItem {
  to: string;
  label: string;
  icon: typeof OverviewIcon;
  /** Hidden when the caller lacks it. A convenience, never the enforcement. */
  permission?: string;
}

const NAV: NavItem[] = [
  { to: "/", label: "Overview", icon: OverviewIcon },
  { to: "/expenses", label: "Expenses", icon: ExpensesIcon },
  { to: "/approvals", label: "Approvals", icon: ApprovalsIcon, permission: "expense:approve" },
  { to: "/budgets", label: "Budgets", icon: BudgetsIcon, permission: "expense:read:team" },
  { to: "/organisation", label: "Organisation", icon: OrganisationIcon, permission: "member:manage" },
];

/**
 * The scene and the line under it change with the section.
 *
 * Which is the one thing in the sidebar that is not navigation: it gives each
 * screen a different weather, so a glance at the edge of the window tells you
 * where you are before you have read a word.
 */
const SCENES: Array<{ match: (path: string) => boolean; scene: Scene; caption: string }> = [
  { match: (p) => p.startsWith("/expenses"), scene: "ridge", caption: "Small spending. Big progress." },
  { match: (p) => p.startsWith("/approvals"), scene: "arch", caption: "Faster decisions. Bigger ideas." },
  { match: (p) => p.startsWith("/budgets"), scene: "road", caption: "Ideas, resourced for reality." },
  { match: (p) => p.startsWith("/organisation"), scene: "crescent", caption: "People drive possibilities." },
  { match: () => true, scene: "crescent", caption: "A clear view of the month." },
];

export function AppShell() {
  const profile = useProfile();
  const { pathname } = useLocation();

  // Read once for the whole list rather than through a hook per item: the
  // number of navigation entries is not allowed to change how many hooks this
  // component calls.
  const permissions = useSessionStore((s) => s.profile?.permissions);
  const visible = NAV.filter(
    (item) => !item.permission || (permissions?.includes(item.permission) ?? false),
  );

  const art = SCENES.find((s) => s.match(pathname)) ?? SCENES[SCENES.length - 1];

  return (
    <div className="flex min-h-dvh">
      {/* A skip link, first in the tab order. Without it, reaching the table on
          a page with a dozen navigation items means a dozen tab presses on
          every navigation. */}
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:top-4 focus:left-4 focus:z-50 focus:rounded-lg focus:bg-elevated focus:px-4 focus:py-2"
      >
        Skip to content
      </a>

      <aside className="sticky top-0 hidden h-dvh w-52 shrink-0 flex-col border-r border-line-soft bg-rail lg:flex">
        <div className="flex h-14 items-center gap-2.5 px-5">
          <LogoMark className="size-6 text-accent" />
          <span className="text-sm font-semibold tracking-tight">
            {profile?.tenant_name ?? "Expenses"}
          </span>
        </div>

        <nav aria-label="Main" className="flex flex-col gap-px px-2.5 py-1">
          {visible.map(({ to, label, icon: Glyph }) => (
            <NavLink
              key={to}
              to={to}
              end={to === "/"}
              className={({ isActive }) =>
                `flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] transition-colors ${
                  isActive
                    ? "bg-elevated font-medium text-fg"
                    : "text-muted hover:bg-surface hover:text-fg"
                }`
              }
            >
              <Glyph className="size-4 shrink-0" />
              {label}
            </NavLink>
          ))}
        </nav>

        <RailArt scene={art.scene} caption={art.caption} />
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-4 border-b border-line-soft bg-canvas/85 px-4 backdrop-blur-md sm:px-6">
          {/* The mark repeats here only where the sidebar is not on screen. */}
          <span className="flex items-center gap-2.5 lg:hidden">
            <LogoMark className="size-6 text-accent" />
            <span className="text-sm font-semibold">{profile?.tenant_name ?? "Expenses"}</span>
          </span>

          <GlobalSearch />

          <div className="ml-auto flex items-center gap-1.5">
            <NotificationBell />
            <AccountMenu />
          </div>
        </header>

        <main id="main" className="flex-1 px-4 py-6 sm:px-6">
          <div className="mx-auto max-w-6xl">
            <Outlet />
          </div>
        </main>

        {/* The navigation, for the widths that have no room for a rail. */}
        <nav
          aria-label="Main"
          className="sticky bottom-0 z-30 flex justify-around border-t border-line-soft bg-canvas/95 backdrop-blur-md lg:hidden"
        >
          {visible.map(({ to, label, icon: Glyph }) => (
            <NavLink
              key={to}
              to={to}
              end={to === "/"}
              className={({ isActive }) =>
                `flex flex-1 flex-col items-center gap-1 py-2.5 text-[11px] ${
                  isActive ? "text-accent" : "text-faint"
                }`
              }
            >
              <Glyph className="size-5" />
              {label}
            </NavLink>
          ))}
        </nav>
      </div>
    </div>
  );
}
