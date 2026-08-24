"use client";

import { useEffect, useId, useRef, useState } from "react";
import { usePeopleSearch } from "@/lib/usePeopleSearch";

// EmailSuggest is the group-membership input with a people typeahead attached.
//
// It suggests; it does not constrain. The field accepts emails AND globs
// (`*@example.com`, `*`), and a person who has never signed in is a legitimate
// member — arti grants to an address, not to an account — so anything typed
// still submits. What the dropdown removes is the case where a misspelled
// address looks identical to a colleague who simply hasn't logged in yet.
//
// ↑/↓ move the highlight, Enter takes it (or submits the raw text when nothing
// is highlighted), Esc closes the list without closing anything around it.
export function EmailSuggest({
  value,
  onChange,
  onSubmit,
  exclude,
  disabled,
  placeholder,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
  /** Members already on the list — suggesting one wastes a dropdown slot. */
  exclude: string[];
  disabled?: boolean;
  placeholder?: string;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const wrapRef = useRef<HTMLDivElement>(null);
  // The listbox needs a DOM id so the input can point aria-controls at it;
  // useId keeps it unique when two of these render on one page.
  const listId = useId();
  const matches = usePeopleSearch(value, exclude);
  const shown = open && matches.length > 0;

  // Reset the highlight whenever the candidate set changes: an index left over
  // from a longer list can point past the end of a shorter one, which makes
  // Enter select nothing while appearing to select a row.
  //
  // Adjusted during render rather than in an effect — the same pattern
  // AccessModal uses to resync from props. An effect would render the stale
  // highlight once before correcting it, and that frame is exactly the one a
  // fast typist presses Enter in.
  const [seenCount, setSeenCount] = useState(matches.length);
  if (seenCount !== matches.length) {
    setSeenCount(matches.length);
    setActive(-1);
  }

  // Click-outside closes. On the document because the list is an overlay: a
  // blur handler alone fires before the click reaches a row.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);

  const pick = (email: string) => {
    onChange(email);
    setOpen(false);
    setActive(-1);
  };

  return (
    <div className="relative min-w-0 flex-1" ref={wrapRef}>
      <input
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" && shown) {
            e.preventDefault();
            setActive((i) => (i + 1) % matches.length);
          } else if (e.key === "ArrowUp" && shown) {
            e.preventDefault();
            setActive((i) => (i <= 0 ? matches.length - 1 : i - 1));
          } else if (e.key === "Escape" && shown) {
            // Contained: the same key closes the dialog this field can sit in,
            // and dismissing a dropdown should not also discard the dialog.
            e.preventDefault();
            e.stopPropagation();
            setOpen(false);
          } else if (e.key === "Enter") {
            e.preventDefault();
            if (shown && active >= 0) {
              pick(matches[active]);
              return;
            }
            setOpen(false);
            onSubmit();
          }
        }}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete="off"
        role="combobox"
        aria-expanded={shown}
        aria-controls={listId}
        aria-autocomplete="list"
        className={
          className ??
          "w-full min-w-0 rounded-md border border-neutral-200 bg-white px-2 py-1 font-sans text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
        }
      />
      {shown ? (
        <ul
          id={listId}
          role="listbox"
          aria-label="matching people"
          className="absolute left-0 right-0 top-full z-20 mt-1 max-h-56 overflow-y-auto rounded-md border border-neutral-200 bg-white py-1 shadow-lg"
        >
          {matches.map((email, i) => (
            <li key={email}>
              <button
                type="button"
                role="option"
                aria-selected={i === active}
                // mousedown, not click: click fires after blur, by which point
                // the list can already be gone.
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(email);
                }}
                onMouseEnter={() => setActive(i)}
                className={
                  "flex w-full items-center gap-2 px-2.5 py-1 text-left text-[12px] text-neutral-800 " +
                  (i === active ? "bg-neutral-100" : "")
                }
              >
                <span className="truncate font-sans">{email}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
