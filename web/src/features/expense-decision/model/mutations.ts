import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api } from "@/shared/api";
import { budgetKeys } from "@/entities/budget";
import { expenseKeys, type ExpenseAction } from "@/entities/expense";

export interface Decision {
  action: ExpenseAction;
  /** Only rejecting and settling carry one; see PROMPTS. */
  value?: string;
}

/** The body field each action's text belongs in, as the server names it. */
const FIELD: Partial<Record<ExpenseAction, "reason" | "payment_ref">> = {
  reject: "reason",
  pay: "payment_ref",
};

export function useExpenseAction(id: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ action, value }: Decision) => {
      const field = FIELD[action];
      const body = field && value ? { [field]: value } : undefined;
      await api.post(`/expenses/${id}/${action}`, body);
    },

    // Invalidated rather than patched from the response. A transition may have
    // changed the revision, the timestamps and what is allowed next, and the
    // ledger has gained a row - reconstructing that on the client is a second
    // copy of rules the server already applied.
    //
    // onSettled, not onSuccess: a 409 means somebody else moved the claim, so
    // what is on screen is stale in exactly the case where it matters most.
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: expenseKeys.all });
      // An approval moves money against a budget, so the consumption figures
      // on the overview are stale too.
      await queryClient.invalidateQueries({ queryKey: budgetKeys.all });
    },
  });
}

export function useDeleteExpense(id: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => {
      await api.delete(`/expenses/${id}`);
    },
    onSuccess: async () => {
      queryClient.removeQueries({ queryKey: expenseKeys.detail(id) });
      await queryClient.invalidateQueries({ queryKey: expenseKeys.lists() });
    },
  });
}
