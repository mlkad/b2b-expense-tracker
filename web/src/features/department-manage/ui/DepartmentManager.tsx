import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";

import { departmentsQuery } from "@/entities/department";
import { ApiError } from "@/shared/api";
import { useFormErrors } from "@/shared/lib/form";
import { Button, Card, EmptyState, ErrorNotice, Field, SkeletonRows, TextInput } from "@/shared/ui/kit";
import { Monogram } from "@/shared/ui/Monogram";

import {
  departmentSchema,
  useArchiveDepartment,
  useCreateDepartment,
} from "../model/mutations";

export function DepartmentManager() {
  const form = useFormErrors(departmentSchema);
  const create = useCreateDepartment();
  const archive = useArchiveDepartment();
  const departments = useQuery(departmentsQuery());

  const [name, setName] = useState("");

  const failure = form.message ?? (archive.error instanceof ApiError ? archive.error.message : null);
  const loadFailure = departments.error;

  async function onSubmit(event: FormEvent) {
    event.preventDefault();

    const values = form.validate({ name });
    if (!values) return;

    try {
      await create.mutateAsync(values);
      setName("");
      form.reset();
    } catch (err) {
      form.capture(err);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {(failure ?? loadFailure) && (
        <ErrorNotice
          title={
            form.failure?.isPlanLimit
              ? "No departments left on your plan"
              : "Could not update the departments"
          }
          detail={failure ?? loadFailure?.message}
          traceId={form.failure?.traceId}
        />
      )}

      <Card className="p-5">
        <form onSubmit={onSubmit} className="flex items-end gap-3" noValidate>
          <div className="flex-1">
            <Field label="New department" htmlFor="dept-name" error={form.fields.name}>
              <TextInput
                id="dept-name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                invalid={Boolean(form.fields.name)}
              />
            </Field>
          </div>
          <Button type="submit" busy={create.isPending}>
            Add
          </Button>
        </form>
      </Card>

      <Card>
        {departments.isPending ? (
          <SkeletonRows rows={3} columns={2} />
        ) : (departments.data?.length ?? 0) === 0 ? (
          <EmptyState
            title="No departments"
            detail="Claims can be filed without one, but budgets need them."
          />
        ) : (
          <ul className="divide-y divide-line-soft">
            {(departments.data ?? []).map((department) => (
              <li
                key={department.id}
                className="flex items-center justify-between px-5 py-3 transition-colors hover:bg-surface/70"
              >
                <span className="flex items-center gap-2.5 text-sm font-medium">
                  <Monogram name={department.name} />
                  {department.name}
                </span>
                <Button variant="ghost" onClick={() => archive.mutate(department.id)}>
                  Archive
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <p className="text-xs text-muted">
        Archiving retires a department without deleting it. Claims filed against it stay
        attributable, which is the point of having had one.
      </p>
    </div>
  );
}
