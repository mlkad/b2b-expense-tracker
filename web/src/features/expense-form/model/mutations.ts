import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, decode } from "@/shared/api";
import { expenseKeys, expenseSchema } from "@/entities/expense";

import type { ExpenseDraft } from "./schema";

function bodyFor(draft: ExpenseDraft, currency: string) {
  return {
    department_id: draft.department_id || null,
    category: draft.category,
    amount_minor: draft.amount,
    currency,
    merchant: draft.merchant,
    description: draft.description || null,
    spent_at: draft.spent_at,
  };
}

export function useSaveExpense(id: string | undefined, currency: string) {
  const queryClient = useQueryClient();
  const editing = Boolean(id);

  return useMutation({
    mutationFn: async (draft: ExpenseDraft) => {
      const body = bodyFor(draft, currency);
      const response = editing
        ? await api.patch(`/expenses/${id}`, body)
        : await api.post("/expenses", body);
      return decode(expenseSchema, response, editing ? "PATCH /expenses/:id" : "POST /expenses");
    },
    // Invalidated, not primed with the response.
    //
    // Create and update answer with the claim alone; only the detail endpoint
    // computes allowed_actions, because deciding what this caller may do next
    // is the state machine's job and it runs on a read. Writing the smaller
    // response into the detail cache leaves the page it navigates to with no
    // action buttons at all until the entry goes stale.
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: expenseKeys.all });
    },
  });
}
