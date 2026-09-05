import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { z } from "zod";

import { ApiError } from "@/shared/api";

import { useFormErrors } from "./form";

const schema = z.object({
  email: z.email("Enter an email address."),
  amount: z.string().transform((value, ctx) => {
    const parsed = Number(value);
    if (!Number.isInteger(parsed) || parsed <= 0) {
      ctx.addIssue({ code: "custom", message: "Enter a whole amount." });
      return z.NEVER;
    }
    return parsed;
  }),
});

describe("useFormErrors", () => {
  it("returns the parsed values when the form is valid", () => {
    const { result } = renderHook(() => useFormErrors(schema));

    let values: { email: string; amount: number } | null = null;
    act(() => {
      values = result.current.validate({ email: "a@b.co", amount: "40" });
    });

    // Parsed, not merely checked: what leaves the schema is the integer the
    // server stores, so the string never reaches the arithmetic.
    expect(values).toEqual({ email: "a@b.co", amount: 40 });
    expect(result.current.fields).toEqual({});
  });

  it("keys each message to its field", () => {
    const { result } = renderHook(() => useFormErrors(schema));

    act(() => {
      expect(result.current.validate({ email: "not-an-email", amount: "12.5" })).toBeNull();
    });

    expect(result.current.fields.email).toBe("Enter an email address.");
    expect(result.current.fields.amount).toBe("Enter a whole amount.");
  });

  /**
   * Both halves are needed and neither replaces the other: the schema catches
   * an empty field without a round trip, and the server catches what only it
   * can know - that an address is already taken.
   */
  it("keeps what the server rejected, beside the field it names", () => {
    const { result } = renderHook(() => useFormErrors(schema));

    act(() => {
      result.current.validate({ email: "taken@acme.test", amount: "40" });
    });
    act(() => {
      result.current.capture(
        new ApiError(422, "the request could not be processed", [
          { field: "email", detail: "that address is already a member" },
        ]),
      );
    });

    expect(result.current.fields.email).toBe("that address is already a member");
    // A failure about one field is placed on it, not repeated as a banner.
    expect(result.current.message).toBeNull();
  });

  it("shows a failure with no field as a banner", () => {
    const { result } = renderHook(() => useFormErrors(schema));

    act(() => {
      result.current.capture(new ApiError(402, "the free plan includes 1 department"));
    });

    expect(result.current.message).toBe("the free plan includes 1 department");
    expect(result.current.failure?.isPlanLimit).toBe(true);
  });

  it("clears the previous failure when the form is submitted again", () => {
    const { result } = renderHook(() => useFormErrors(schema));

    act(() => {
      result.current.capture(new ApiError(422, "no", [{ field: "email", detail: "taken" }]));
    });
    act(() => {
      result.current.validate({ email: "new@acme.test", amount: "40" });
    });

    // Otherwise the message about the old address stays under the new one.
    expect(result.current.fields.email).toBeUndefined();
  });

  it("rethrows anything that is not a response", () => {
    const { result } = renderHook(() => useFormErrors(schema));

    // A TypeError here is a bug rather than a rejection, and swallowing it
    // would render a form that merely looks like it failed to save.
    expect(() => result.current.capture(new TypeError("undefined is not a function"))).toThrow(
      TypeError,
    );
  });
});
