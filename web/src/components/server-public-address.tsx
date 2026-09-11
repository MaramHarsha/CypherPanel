// A server's public address — where DNS records for its applications point
// (dns-automation.md §3.4).
//
// It is an editable Fact rather than a form: one value, changed rarely, and
// nothing else on the card is editable. The panel cannot discover this itself
// (the agent dials out and the heartbeat carries no address), so the copy says
// what it is for rather than leaving the operator to guess why it is asked for.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { getGetServerQueryKey, getListServersQueryKey, useUpdateServer } from "@/api/gen/servers/servers";
import { ActionButton } from "@/components/ui/action-button";
import { Input } from "@/components/ui/input";
import { toastSuccess } from "@/lib/toast";

export function ServerPublicAddress({ serverId, value }: { serverId: string; value: string }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [address, setAddress] = useState(value);
  const [error, setError] = useState<string | null>(null);

  const save = useUpdateServer({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetServerQueryKey(serverId) });
        void qc.invalidateQueries({ queryKey: getListServersQueryKey() });
        setEditing(false);
        setError(null);
        toastSuccess("Public address saved");
      },
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not save the address"),
    },
  });

  if (!editing) {
    return (
      <span className="flex items-center gap-2">
        <span className="mono">{value || "—"}</span>
        <button
          type="button"
          onClick={() => {
            setAddress(value);
            setEditing(true);
          }}
          className="text-[11.5px] font-medium text-text-dim underline-offset-2 hover:underline"
        >
          {value ? "Change" : "Set"}
        </button>
      </span>
    );
  }

  return (
    <span className="flex flex-col gap-1.5">
      <span className="flex items-center gap-1.5">
        <Input
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          placeholder="203.0.113.7"
          aria-label="Public address"
          className="mono h-7 w-40"
          autoFocus
        />
        <ActionButton
          variant="primary"
          state={save.isPending ? "busy" : "idle"}
          busyLabel="Saving…"
          onClick={() => {
            setError(null);
            save.mutate({ id: serverId, data: { public_address: address.trim() } });
          }}
        >
          Save
        </ActionButton>
        <ActionButton variant="ghost" onClick={() => setEditing(false)}>
          Cancel
        </ActionButton>
      </span>
      {error ? (
        <span role="alert" className="text-[11.5px] text-danger">
          {error}
        </span>
      ) : (
        <span className="text-[11.5px] text-text-faint">
          Where DNS records for this server's apps point. Leave it empty and no records are written for them.
        </span>
      )}
    </span>
  );
}
