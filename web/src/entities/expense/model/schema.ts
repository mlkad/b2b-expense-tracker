import { z } from "zod";

import { isoDate, moneySchema, timestamp } from "@/shared/api";

export const expenseStatusSchema = z.enum([
  "draft",
  "pending_approval",
  "approved",
  "rejected",
  "paid",
]);

export const expenseActionSchema = z.enum([
  "submit",
  "approve",
  "reject",
  "withdraw",
  "revise",
  "pay",
]);

export const expenseCategorySchema = z.enum([
  "travel",
  "meals",
  "accommodation",
  "software",
  "hardware",
  "marketing",
  "training",
  "office",
  "contractor",
  "other",
]);

export const expenseSchema = z.object({
  id: z.string(),
  submitter_id: z.string(),
  department_id: z.string().nullish(),
  status: expenseStatusSchema,
  category: expenseCategorySchema,
  amount: moneySchema,
  merchant: z.string(),
  description: z.string().nullish(),
  spent_at: isoDate,
  submitted_at: timestamp.nullish(),
  decided_at: timestamp.nullish(),
  decision_note: z.string().nullish(),
  paid_at: timestamp.nullish(),
  payment_ref: z.string().nullish(),
  revision: z.number().int(),
  version: z.number().int(),
  created_at: timestamp,
  updated_at: timestamp,
  /**
   * Exactly what this caller may do to this claim right now, computed by the
   * state machine on the server. The dashboard renders one button per entry
   * and does not decide for itself - a second copy of the transition rules in
   * TypeScript would drift, and the symptom would be a button that 403s.
   */
  allowed_actions: z.array(expenseActionSchema).optional(),
});

export const expenseEventSchema = z.object({
  id: z.number().int(),
  action: z.string(),
  from_status: expenseStatusSchema.nullish(),
  to_status: expenseStatusSchema,
  actor_email: z.string().nullish(),
  reason: z.string().nullish(),
  amount: moneySchema,
  revision: z.number().int(),
  occurred_at: timestamp,
});

export const attachmentSchema = z.object({
  id: z.string(),
  expense_id: z.string(),
  filename: z.string(),
  content_type: z.string(),
  size_bytes: z.number().int(),
  /** Hex, so it matches what sha256sum prints. */
  checksum: z.string(),
  uploaded_by: z.string(),
  created_at: timestamp,
});

export const uploadTicketSchema = z.object({
  object_key: z.string(),
  upload: z.object({
    url: z.string(),
    method: z.string(),
    /** Sent verbatim: they are covered by the signature. */
    headers: z.record(z.string(), z.string()),
    expires_at: timestamp,
  }),
});

export type Expense = z.infer<typeof expenseSchema>;
export type ExpenseStatus = z.infer<typeof expenseStatusSchema>;
export type ExpenseAction = z.infer<typeof expenseActionSchema>;
export type ExpenseCategory = z.infer<typeof expenseCategorySchema>;
export type ExpenseEventRecord = z.infer<typeof expenseEventSchema>;
export type Attachment = z.infer<typeof attachmentSchema>;
export type UploadTicket = z.infer<typeof uploadTicketSchema>;

export const EXPENSE_CATEGORIES = expenseCategorySchema.options;
export const EXPENSE_STATUSES = expenseStatusSchema.options;
