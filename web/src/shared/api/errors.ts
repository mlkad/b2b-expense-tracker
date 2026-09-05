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
