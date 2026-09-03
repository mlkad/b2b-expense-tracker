import { useState, type FormEvent } from "react";

import { ApiError } from "../api/client";
import { Button, Card, ErrorNotice, Field, TextInput } from "../components/ui";
import { useSession } from "../auth/context";

export function SignIn() {
  const { signIn } = useSession();

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [organisation, setOrganisation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  /**
   * The organisation field appears only once the server asks for it.
   *
   * Most people belong to one organisation and should not be made to think
   * about which. The server answers with a field error naming "organisation"
   * when a user belongs to several, and that is the moment to show the input.
   */
  const needsOrganisation = error?.fieldError("organisation") !== undefined;

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);

    try {
      await signIn(email, password, organisation.trim() || undefined);
    } catch (err) {
      if (err instanceof ApiError) setError(err);
      else throw err;
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="mx-auto flex min-h-dvh max-w-sm flex-col justify-center px-6">
      <h1 className="mb-1 text-xl font-semibold">Expense Tracker</h1>
      <p className="mb-6 text-sm text-ink-600">Sign in to your organisation.</p>

      <Card className="p-6">
        <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
          {error && !needsOrganisation && (
            <ErrorNotice
              title="Could not sign in"
              detail={error.message}
              traceId={error.traceId}
            />
          )}
          {needsOrganisation && (
            <ErrorNotice
              title="Which organisation?"
              detail={error?.fieldError("organisation")}
            />
          )}

          <Field label="Email" htmlFor="email" error={error?.fieldError("email")}>
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
              invalid={Boolean(error?.fieldError("email"))}
            />
          </Field>

          <Field label="Password" htmlFor="password" error={error?.fieldError("password")}>
            <TextInput
              id="password"
              name="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              invalid={Boolean(error?.fieldError("password"))}
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

          <Button type="submit" busy={busy} className="mt-2 w-full">
            Sign in
          </Button>
        </form>
      </Card>
    </main>
  );
}
