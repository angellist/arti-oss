"use client";

import { MENU_ROW, MenuIcon } from "./MenuRow";

// CommentsMenuItem — the per-document "Allow / disallow comments" row in the
// viewer's ⋮ menu.
//
// Two audiences, one row. The OWNER uses it as a switch. Everyone else reads it
// as a status line: the dot is the answer to "why is there nowhere to comment
// on this doc?", which is otherwise indistinguishable from a bug. So the row is
// rendered for both, and only the click is gated — `canManage` is the server's
// own verdict (`can_manage_comments`), never re-derived on the client.
//
// It lives in its own file rather than inline in the menu so it can be rendered
// and asserted on directly; the menu body itself only exists after a click,
// which the static-markup tests can't perform.
export default function CommentsMenuItem({
  on,
  canManage,
  busy,
  onToggle,
}: {
  on: boolean;
  canManage: boolean;
  busy?: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      onClick={canManage ? onToggle : undefined}
      disabled={!canManage || busy}
      aria-pressed={on}
      aria-label={on ? "comments allowed" : "comments turned off"}
      className={
        MENU_ROW + " whitespace-nowrap " +
        (canManage ? "hover:bg-neutral-50 disabled:opacity-50" : "cursor-default")
      }
      title={
        canManage
          ? on
            ? "disallow comments — removes every comment control on all versions of this doc"
            : "allow comments on this doc (existing comments reappear)"
          : on
            ? "comments are allowed — only the doc's owner can change this"
            : "the owner turned comments off for this doc"
      }
    >
      {/* The state dot sits centered in the same 14px slot the other rows use
          for their glyph, so "Comments" lines up with "Edit" / "Download"
          instead of being pushed left by a 6px dot. */}
      <MenuIcon>
        <span
          className={
            "inline-block h-1.5 w-1.5 rounded-full " +
            (on ? "bg-emerald-500" : "bg-neutral-300")
          }
        />
      </MenuIcon>
      Comments
      <span className="ml-auto pl-4 text-[11px] text-neutral-400">
        {busy ? "saving…" : on ? "Allowed" : "Off"}
      </span>
    </button>
  );
}
