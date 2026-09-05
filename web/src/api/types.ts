/**
 * The shapes the API returns.
 *
 * Hand-written rather than generated from api/openapi.json. The document
 * describes every path, parameter and status code, but most request bodies are
 * declared as a bare object - so generating types from it today would produce
 * `unknown` where the useful information is, and would look like type safety
 * without being any. Filling the schemas in properly is worth doing; until it
 * is done, these are honest declarations that the integration tests exercise.
 */

/** An exact amount. Never a float: see the note on formatMoney. */
export interface Money {
  amount_minor: number;
  currency: string;
  /** Rendered by the server with the currency's own exponent. */
  formatted: string;
}

export type ExpenseStatus =
  | "draft"
  | "pending_approval"
  | "approved"
  | "rejected"
  | "paid";

export type ExpenseAction =
  | "submit"
  | "approve"
  | "reject"
  | "withdraw"
  | "revise"
  | "pay";

export type ExpenseCategory =
  | "travel"
  | "meals"
  | "accommodation"
  | "software"
  | "hardware"
  | "marketing"
  | "training"
  | "office"
  | "contractor"
  | "other";

export type Role = "owner" | "admin" | "finance" | "manager" | "member" | "viewer";

export interface Expense {
  id: string;
  submitter_id: string;
  department_id?: string | null;
  status: ExpenseStatus;
  category: ExpenseCategory;
  amount: Money;
  merchant: string;
  description?: string | null;
  spent_at: string;
  submitted_at?: string | null;
  decided_at?: string | null;
  decision_note?: string | null;
  paid_at?: string | null;
  payment_ref?: string | null;
  revision: number;
  version: number;
  created_at: string;
  updated_at: string;
  /**
   * Exactly what this caller may do to this claim right now, computed by the
   * state machine on the server. The dashboard renders one button per entry
   * and does not decide for itself - a second copy of the transition rules in
   * TypeScript would drift, and the symptom would be a button that 403s.
   */
  allowed_actions?: ExpenseAction[];
}

export interface Page<T> {
  items: T[];
  has_more: boolean;
  next_cursor?: string;
}

export interface Profile {
  user_id: string;
  email: string;
  full_name?: string;
  tenant_id: string;
  tenant_slug: string;
  tenant_name: string;
  default_currency: string;
  membership_id: string;
  role: Role;
  status: string;
  department_id?: string | null;
  /** Negative means unlimited. */
  approval_limit_minor: number;
  permissions: string[];
}

export interface Department {
  id: string;
  name: string;
  parent_id?: string | null;
  archived_at?: string | null;
}

export interface BudgetConsumption {
  budget_id: string;
  department_id?: string | null;
  department_name?: string | null;
  period_start: string;
  period_end: string;
  budget: Money;
  consumed: Money;
  remaining: Money;
  claim_count: number;
  usage_bps: number;
  alert_threshold_bps: number;
  breached: boolean;
}

export interface StatusTotal {
  status: ExpenseStatus;
  claim_count: number;
  total: Money;
}

export interface DepartmentTotal {
  department_id?: string | null;
  department_name: string;
  claim_count: number;
  total: Money;
}

export interface Summary {
  by_status: StatusTotal[];
  by_department: DepartmentTotal[];
}

export interface Entitlement {
  plan: string;
  status: string;
  known: boolean;
  in_grace_period: boolean;
  needs_checkout: boolean;
  current_period_end: string;
  cancel_at_period_end: boolean;
  limits: {
    Seats: number;
    Departments: number;
    VendorSubscriptions: number;
    ExportRows: number;
  };
}

export interface ExpenseEventRecord {
  id: number;
  action: string;
  from_status?: ExpenseStatus | null;
  to_status: ExpenseStatus;
  actor_email?: string | null;
  reason?: string | null;
  amount: Money;
  revision: number;
  occurred_at: string;
}

export interface Session {
  access_token: string;
  expires_at: string;
  tenant_id: string;
  tenant_slug: string;
  role: Role;
}

/** One field the server rejected, so the message can sit next to its input. */
export interface FieldError {
  field: string;
  detail: string;
}

export interface ErrorBody {
  status: number;
  message: string;
  fields?: FieldError[];
  trace_id?: string;
}

export interface Attachment {
  id: string;
  expense_id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  /** Hex, so it matches what sha256sum prints. */
  checksum: string;
  uploaded_by: string;
  created_at: string;
}

export interface UploadTicket {
  object_key: string;
  upload: {
    url: string;
    method: string;
    /** Sent verbatim: they are covered by the signature. */
    headers: Record<string, string>;
    expires_at: string;
  };
}

export interface Budget {
  id: string;
  department_id?: string | null;
  period_start: string;
  period_end: string;
  amount: Money;
  alert_threshold_bps: number;
}

export interface Member {
  id: string;
  user_id: string;
  email: string;
  full_name?: string | null;
  role: Role;
  status: string;
  department_id?: string | null;
  department_name?: string | null;
  approval_limit_minor?: number | null;
}

export interface VendorSubscription {
  id: string;
  vendor: string;
  plan_name?: string | null;
  department_id?: string | null;
  department_name?: string | null;
  amount: Money;
  cadence: "weekly" | "monthly" | "quarterly" | "annual";
  status: "active" | "paused" | "cancelled";
  next_charge_on: string;
  auto_create_expense: boolean;
  annualised_minor: number;
}
