import { useEffect, useRef, type ReactNode } from "react";

/**
 * A dialog built on <dialog>, so the browser supplies the things a div cannot:
 * focus is trapped inside it, Escape closes it, and the rest of the page is
 * inert to a screen reader while it is open.
 */
export function Modal({
  open,
  title,
  onClose,
  children,
}: {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;

    // showModal rather than the open attribute: only the former makes the
    // backdrop inert and traps focus.
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      aria-labelledby="modal-title"
      // The browser fires close for Escape too, so cancelling and closing
      // converge on one path rather than two that can disagree.
      onClose={onClose}
      onClick={(event) => {
        // A click on the backdrop lands on the dialog element itself; one on
        // the content lands on a child. Comparing the target is what tells
        // them apart without a wrapper div that would break the backdrop.
        if (event.target === ref.current) onClose();
      }}
      className="w-full max-w-md rounded-lg border border-ink-100 p-0 backdrop:bg-ink-900/40 open:m-auto"
    >
      <div className="flex flex-col gap-4 p-6">
        <h2 id="modal-title" className="text-base font-semibold">
          {title}
        </h2>
        {children}
      </div>
    </dialog>
  );
}
