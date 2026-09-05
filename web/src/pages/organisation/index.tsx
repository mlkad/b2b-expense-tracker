import { useState } from "react";
import { parseAsStringLiteral, useQueryState } from "nuqs";

import { DepartmentManager } from "@/features/department-manage";
import { sentenceCase } from "@/shared/lib/format";
import { PageHeader } from "@/shared/ui/PageHeader";
import { Button } from "@/shared/ui/kit";
import { PlusIcon } from "@/shared/ui/icons";

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

  // Held here rather than inside the panel, because the control that opens the
  // form sits on the tab row - which belongs to the page.
  const [inviting, setInviting] = useState(false);

  return (
    <div>
      <PageHeader title="Organisation" />

      {/* Real tabs: the roles are what let a screen reader announce this as a
          tab list rather than as five unrelated buttons above some content. */}
      <div className="mb-6 flex items-end justify-between gap-4 border-b border-line-soft">
      <div role="tablist" aria-label="Organisation sections" className="flex gap-6">
        {TABS.map((key) => (
          <button
            key={key}
            role="tab"
            type="button"
            aria-selected={tab === key}
            aria-controls={`panel-${key}`}
            id={`tab-${key}`}
            onClick={() => void setTab(key)}
            className={`-mb-px border-b-2 pb-3 text-sm transition-colors ${
              tab === key
                ? "border-accent font-medium text-fg"
                : "border-transparent text-muted hover:text-fg"
            }`}
          >
            {sentenceCase(key)}
          </button>
        ))}
      </div>

        {tab === "members" && (
          <Button className="mb-2" onClick={() => setInviting((open) => !open)}>
            {!inviting && <PlusIcon className="size-4" />}
            {inviting ? "Cancel" : "Invite"}
          </Button>
        )}
      </div>

      <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
        {tab === "members" && (
          <MembersPanel inviting={inviting} onDone={() => setInviting(false)} />
        )}
        {tab === "departments" && <DepartmentManager />}
        {tab === "subscriptions" && <SubscriptionsPanel />}
      </div>
    </div>
  );
}
