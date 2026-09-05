import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";

import { SearchIcon } from "@/shared/ui/icons";

/**
 * Search is the expense list's free-text filter, reachable from anywhere.
 *
 * Not a separate results page. There is one collection here worth searching -
 * claims - and sending the query to the screen that can already filter, sort
 * and export it gives the reader somewhere to go next, which a list of bare
 * results does not.
 */
export function GlobalSearch() {
  const input = useRef<HTMLInputElement>(null);
  const [term, setTerm] = useState("");
  const navigate = useNavigate();

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "k" || !(event.metaKey || event.ctrlKey)) return;
      // Claiming the shortcut means taking it from the browser's own find bar
      // on some platforms, so it is only worth doing if the field is focused
      // reliably afterwards.
      event.preventDefault();
      input.current?.focus();
      input.current?.select();
    }

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    const query = term.trim();
    void navigate(query ? `/expenses?q=${encodeURIComponent(query)}` : "/expenses");
  }

  return (
    <form onSubmit={onSubmit} role="search" className="hidden max-w-md flex-1 sm:block">
      <div className="relative">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-faint" />
        <input
          ref={input}
          type="search"
          value={term}
          onChange={(e) => setTerm(e.target.value)}
          placeholder="Search anything..."
          aria-label="Search expense claims"
          className="h-9 w-full rounded-full border border-line-soft bg-surface pr-16 pl-9 text-[13px] text-fg outline-none transition-colors placeholder:text-faint hover:border-line focus:border-accent-strong"
        />
        {/* aria-hidden: it is a hint about a shortcut, and read aloud in the
            middle of a text field it is just noise. */}
        <kbd
          aria-hidden="true"
          className="absolute top-1/2 right-3 -translate-y-1/2 rounded border border-line px-1.5 py-0.5 font-sans text-[11px] text-faint"
        >
          ⌘K
        </kbd>
      </div>
    </form>
  );
}
