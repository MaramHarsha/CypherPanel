// Panel-role refusals — canvas 13q's 403 for the gates the panel checks itself.
//
// Every other refusal reaches ForbiddenForError as an ApiError, through
// PageState. These settings tabs never make the request: their queries are
// `enabled` on the caller's panel role, so the refusal is known before
// anything is asked, and there is no wire answer to read the fix back off.
// This is the same page, told what the request would have been.
import { useGetMe } from "@/api/gen/auth/auth";
import { useListUsers } from "@/api/gen/teams/teams";
import { ForbiddenPage } from "@/components/error-page";
import { type Role } from "@/lib/roles";

/** A panel-role gate, refused with the fix named (ui-principles §11). */
export function PanelRoleRefusal({ action, needs }: { action: string; needs: Role }) {
  const me = useGetMe();
  // Only an admin may read the user list, so a member is told the fix without
  // a name rather than being refused a second time on this very page — the
  // trade ForbiddenForError already makes for a panel refusal.
  const users = useListUsers({
    query: { enabled: me.data !== undefined && me.data.role !== "member" },
  });
  const owners = (users.data ?? []).filter((u) => u.role === "owner").map((u) => u.email);
  const held = me.data?.role;

  return (
    <ForbiddenPage
      action={action}
      needs={needs}
      held={held}
      scope="panel"
      owners={owners}
      embedded
      // Panel rank is not team rank and there is no API for asking for it, so
      // the ask is an email with the switch to flip already written out.
      onRequestAccess={() => {
        const owner = owners[0];
        if (owner === undefined) return;
        const subject = encodeURIComponent("CypherPanel — access request");
        const body = encodeURIComponent(`${action} needs the ${needs} role${held ? ` — I'm a ${held}` : ""}.`);
        window.location.href = `mailto:${owner}?subject=${subject}&body=${body}`;
      }}
    />
  );
}
