import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api } from "@/shared/api";
import { budgetKeys } from "@/entities/budget";

import type { BudgetDraft } from "./schema";

export function useCreateBudget(currency: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (draft: BudgetDraft) => {
      await api.post("/budgets", {
        department_id: draft.department_id || null,
        period_start: draft.period_start,
        period_end: draft.period_end,
        amount_minor: draft.amount,
        currency,
        alert_threshold_bps: Math.round(draft.alert_threshold * 100),
      });
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: budgetKeys.all }),
  });
}
