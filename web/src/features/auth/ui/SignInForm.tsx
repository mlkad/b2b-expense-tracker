import { useState, type FormEvent } from "react";
import { z } from "zod";

import { Button, Card, ErrorNotice, Field, TextInput } from "@/shared/ui/kit";
import { useFormErrors } from "@/shared/lib/form";

import { useSignIn } from "../model/mutations";

const signInSchema = z.object({
  email: z.email("Enter an email address."),
  // Length is checked on the server against the current policy. Checking a
  // minimum here as well would reject a valid old password with a message
  // about a rule that was introduced after it was set.
  password: z.string().min(1, "Enter your password."),
  organisation: z.string().optional(),
});

export function SignInForm() {
  const signIn = useSignIn();
  const form = useFormErrors(signInSchema);

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [organisation, setOrganisation] = useState("");

  /**
   * The organisation field appears only once the server asks for it.
   *
   * Most people belong to one organisation and should not be made to think
   * about which. The server answers with a field error naming "organisation"
   * when a user belongs to several, and that is the moment to show the input.
   */
  const needsOrganisation = form.fields.organisation !== undefined;

  async function onSubmit(event: FormEvent) {
    event.preventDefault();

    const values = form.validate({ email, password, organisation: organisation.trim() });
    if (!values) return;

    try {
      await signIn.mutateAsync({
        email: values.email,
        password: values.password,
        organisation: values.organisation || undefined,
      });
    } catch (err) {
      form.capture(err);
    }
  }

  return (
    <main className="mx-auto flex min-h-dvh max-w-sm flex-col justify-center px-6">
      <h1 className="mb-1 text-xl font-semibold">Expense Tracker</h1>
      <p className="mb-6 text-sm text-ink-600">Sign in to your organisation.</p>

      <Card className="p-6">
        <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
          {form.message && !needsOrganisation && (
            <ErrorNotice
              title="Could not sign in"
              detail={form.message}
              traceId={form.failure?.traceId}
            />
          )}
          {needsOrganisation && (
            <ErrorNotice title="Which organisation?" detail={form.fields.organisation} />
          )}

          <Field label="Email" htmlFor="email" error={form.fields.email}>
            <TextInput
              id="email"
              name="email"
              type="email"
              // The browser's own credential manager is the right place for
              // these, and the autocomplete tokens are what enable it.
              autoComplete="username"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              invalid={Boolean(form.fields.email)}
            />
          </Field>

          <Field label="Password" htmlFor="password" error={form.fields.password}>
            <TextInput
              id="password"
              name="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              invalid={Boolean(form.fields.password)}
            />
          </Field>

          {needsOrganisation && (
            <Field
              label="Organisation"
              htmlFor="organisation"
              hint="The short name in your organisation's address, such as acme-ltd."
            >
              <TextInput
                id="organisation"
                name="organisation"
                autoComplete="organization"
                value={organisation}
                onChange={(e) => setOrganisation(e.target.value)}
              />
            </Field>
          )}

          <Button type="submit" busy={signIn.isPending} className="mt-2 w-full">
            Sign in
          </Button>
        </form>
      </Card>
    </main>
  );
}
