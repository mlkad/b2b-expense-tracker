import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { budgetConsumptionQuery } from "@/entities/budget";
import { useCan, useProfile } from "@/entities/session";
import { NewBudgetForm } from "@/features/budget-create";
import { ApiError } from "@/shared/api";
import { Button, Card, EmptyState, ErrorNotice, SkeletonRows } from "@/shared/ui/kit";
import { PageHeader } from "@/shared/ui/PageHeader";
import { PlusIcon } from "@/shared/ui/icons";
import { BudgetEnvelope } from "@/widgets/budget-envelope";

export function BudgetsPage() {
  const profile = useProfile();
  const canManage = useCan("budget:manage");
  const [creating, setCreating] = useState(false);

  const consumption = useQuery(budgetConsumptionQuery());
  const envelopes = consumption.data ?? [];

  return (
    <div>
      <PageHeader
        title="Budgets"
        description="Committed means approved and paid claims. Claims awaiting a decision are not counted, so this is money the organisation has agreed to spend."
        actions={
          canManage && (
            <Button onClick={() => setCreating((open) => !open)}>
              {!creating && <PlusIcon className="size-4" />}
              {creating ? "Cancel" : "New budget"}
            </Button>
          )
        }
      />

      <div className="flex flex-col gap-5">

      {consumption.error && (
        <ErrorNotice
          title="Could not load the budgets"
          detail={consumption.error.message}
          traceId={consumption.error instanceof ApiError ? consumption.error.traceId : undefined}
        />
      )}

      {creating && (
        <NewBudgetForm
          currency={profile?.default_currency ?? "USD"}
          onCreated={() => setCreating(false)}
        />
      )}

      {consumption.isPending ? (
        <Card>
          <SkeletonRows rows={3} columns={4} />
        </Card>
      ) : envelopes.length === 0 ? (
        <Card>
          <EmptyState
            title="No budgets set"
            detail="A budget gives a department a ceiling for a period, and raises an alert as it is approached."
          />
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {envelopes.map((envelope) => (
            <BudgetEnvelope key={envelope.budget_id} envelope={envelope} />
          ))}
        </div>
      )}
      </div>
    </div>
  );
}
