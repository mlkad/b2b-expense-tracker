import { useMutation, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";

import { api } from "@/shared/api";
import { departmentKeys } from "@/entities/department";

export const departmentSchema = z.object({
  name: z.string().trim().min(1, "Give the department a name.").max(120),
});

export type DepartmentDraft = z.infer<typeof departmentSchema>;

export function useCreateDepartment() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ name }: DepartmentDraft) => {
      await api.post("/departments", { name });
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: departmentKeys.all }),
  });
}

/**
 * Archiving retires a department without deleting it.
 *
 * Claims filed against it stay attributable, which is the point of having had
 * one - and it is why this is a DELETE that does not delete.
 */
export function useArchiveDepartment() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`/departments/${id}`);
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: departmentKeys.all }),
  });
}
