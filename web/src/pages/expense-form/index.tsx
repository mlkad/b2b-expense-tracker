import { useParams } from "react-router";

import { ExpenseFormFields } from "@/features/expense-form";

export function ExpenseFormPage() {
  const { id } = useParams();
  // Keyed on the route parameter so switching between "new" and an edit
  // rebuilds the form rather than leaving the previous claim's values in it.
  return <ExpenseFormFields key={id ?? "new"} id={id} />;
}
