import { parseAsStringLiteral, useQueryState } from "nuqs";

import { DepartmentManager } from "@/features/department-manage";
import { sentenceCase } from "@/shared/lib/format";

import { MembersPanel } from "./ui/MembersPanel";
import { SubscriptionsPanel } from "./ui/SubscriptionsPanel";

const TABS = ["members", "departments", "subscriptions"] as const;

export function OrganisationPage() {
  // The open tab is in the address bar, so "the subscriptions page" is a link
  // somebody can send. parseAsStringLiteral also means a hand-edited value
  // falls back to the default rather than rendering nothing at all.
  const [tab, setTab] = useQueryState(
    "tab",
    parseAsStringLiteral(TABS).withDefault("members").withOptions({ history: "replace" }),
  );

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-lg font-semibold">Organisation</h1>

      {/* Real tabs: the roles and arrow-key handling are what let a keyboard
          user move between panels the way they expect, rather than tabbing
          through every control in the hidden ones. */}
      <div
        role="tablist"
        aria-label="Organisation sections"
        className="flex gap-1 border-b border-ink-100"
      >
        {TABS.map((key) => (
          <button
            key={key}
            role="tab"
            type="button"
            aria-selected={tab === key}
            aria-controls={`panel-${key}`}
            id={`tab-${key}`}
            onClick={() => void setTab(key)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm ${
              tab === key
                ? "border-brand-600 font-medium text-ink-900"
                : "border-transparent text-ink-600 hover:text-ink-900"
            }`}
          >
            {sentenceCase(key)}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
        {tab === "members" && <MembersPanel />}
        {tab === "departments" && <DepartmentManager />}
        {tab === "subscriptions" && <SubscriptionsPanel />}
      </div>
    </div>
  );
}
