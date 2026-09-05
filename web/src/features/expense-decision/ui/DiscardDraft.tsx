import { useState } from "react";
import { useNavigate } from "react-router";

import { Button } from "@/shared/ui/kit";
import { Modal } from "@/shared/ui/Modal";

import { useDeleteExpense } from "../model/mutations";

/**
 * Only a draft can be discarded, and only by the person who filed it. The
 * server enforces both - through a row-level security policy, not just a
 * check - and this only avoids offering a button that would be refused.
 */
export function DiscardDraft({ id }: { id: string }) {
  const [confirming, setConfirming] = useState(false);
  const remove = useDeleteExpense(id);
  const navigate = useNavigate();

  return (
    <div className="flex items-center justify-between rounded-md border border-line-soft bg-surface px-4 py-3">
      <p className="text-sm text-muted">
        Discard this draft. It has not been submitted, so nothing is lost from the record.
      </p>
      <Button variant="danger" onClick={() => setConfirming(true)}>
        Discard
      </Button>

      <Modal open={confirming} title="Discard this draft?" onClose={() => setConfirming(false)}>
        <p className="text-sm text-muted">This cannot be undone.</p>
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setConfirming(false)}>
            Keep it
          </Button>
          <Button
            variant="danger"
            busy={remove.isPending}
            onClick={async () => {
              await remove.mutateAsync();
              setConfirming(false);
              void navigate("/expenses");
            }}
          >
            Discard
          </Button>
        </div>
      </Modal>
    </div>
  );
}
