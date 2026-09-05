import { useState, type FormEvent } from "react";
import { z } from "zod";

import { Button, Card, ErrorNotice, Field, TextInput } from "@/shared/ui/kit";
import { LogoMark } from "@/shared/ui/icons";
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
    <main className="relative isolate grid min-h-dvh place-items-center px-6 py-12">
      {/* The same scene as the sidebar, so the product looks like itself before
          anybody has signed in to see the rest of it. */}
      <div
        aria-hidden="true"
        className="absolute inset-0 -z-10"
        style={{
          backgroundImage: [
            "radial-gradient(45% 32% at 50% 8%, rgba(139,111,232,0.28), transparent 70%)",
            "radial-gradient(60% 40% at 50% 108%, rgba(203,182,247,0.12), transparent 65%)",
          ].join(", "),
        }}
      />

      <div className="w-full max-w-sm">
      <div className="mb-7 flex flex-col items-center text-center">
        <LogoMark className="size-9 text-accent" />
        <h1 className="mt-4 text-xl font-semibold tracking-tight">Expense Tracker</h1>
        <p className="mt-1 text-sm text-muted">Sign in to your organisation.</p>
      </div>

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
      </div>
    </main>
  );
}
