import { useEffect, useRef, useState } from "react";

import { useProfile } from "@/entities/session";
import { useSignOut } from "@/features/auth";
import { ChevronDownIcon } from "@/shared/ui/icons";

/** Two letters from the name, or one from the address when there is no name. */
function initialsOf(name: string | undefined, email: string): string {
  const words = (name ?? "").trim().split(/\s+/).filter(Boolean);
  if (words.length >= 2) return (words[0][0] + words[words.length - 1][0]).toUpperCase();
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return email.slice(0, 2).toUpperCase();
}

export function AccountMenu() {
  const profile = useProfile();
  const signOut = useSignOut();

  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);

  /**
   * Escape closes it, and so does a click anywhere else.
   *
   * pointerdown rather than click: a click listener fires after the press has
   * already landed on whatever is underneath, so the menu would close *and* the
   * thing behind it would activate.
   */
  useEffect(() => {
    if (!open) return;

    function onPointerDown(event: PointerEvent) {
      if (!wrapper.current?.contains(event.target as Node)) setOpen(false);
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }

    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  if (!profile) return null;

  const name = profile.full_name || profile.email;

  return (
    <div ref={wrapper} className="relative">
      <button
        type="button"
        onClick={() => setOpen((was) => !was)}
        aria-expanded={open}
        aria-haspopup="menu"
        className="flex items-center gap-2 rounded-lg py-1 pr-2 pl-1 transition-colors hover:bg-elevated"
      >
        <span
          aria-hidden="true"
          className="grid size-8 shrink-0 place-items-center rounded-full bg-accent-strong text-[11px] font-semibold text-white"
        >
          {initialsOf(profile.full_name, profile.email)}
        </span>
        <span className="hidden text-left leading-tight sm:block">
          <span className="block text-[12.5px] font-medium leading-tight">{name}</span>
          <span className="block text-[11px] text-faint capitalize">{profile.role}</span>
        </span>
        <ChevronDownIcon className="size-4 text-faint" />
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 z-40 mt-2 w-60 overflow-hidden rounded-xl border border-line bg-elevated shadow-2xl shadow-black/50"
        >
          <div className="border-b border-line-soft px-4 py-3">
            <p className="truncate text-sm font-medium">{name}</p>
            <p className="truncate text-xs text-faint">{profile.email}</p>
          </div>
          <button
            type="button"
            role="menuitem"
            disabled={signOut.isPending}
            onClick={() => signOut.mutate()}
            className="w-full px-4 py-2.5 text-left text-sm text-muted transition-colors hover:bg-surface hover:text-fg disabled:text-faint"
          >
            {signOut.isPending ? "Signing out…" : "Sign out"}
          </button>
        </div>
      )}
    </div>
  );
}
