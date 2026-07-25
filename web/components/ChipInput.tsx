"use client";

import { useMemo, useRef, useState } from "react";

// ChipInput collects a local set of string values (scopes or labels) with a
// typeahead, matching the look of the view-page chip editors. Unlike the
// view page's ChipEditor it does NOT persist on each edit — it just lifts
// the current values via onChange, so the upload modal can submit them all
// at once when the artifact is created.
export default function ChipInput({
  values,
  onChange,
  suggestions,
  placeholder,
  noun,
  chipClass,
}: {
  values: string[];
  onChange: (next: string[]) => void;
  suggestions: string[];
  placeholder: string;
  noun: string;
  chipClass: string;
}) {
  const [input, setInput] = useState("");
  const [focused, setFocused] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const add = (raw: string) => {
    const v = raw.trim();
    if (!v || values.includes(v)) {
      setInput("");
      return;
    }
    onChange([...values, v]);
    setInput("");
  };

  const remove = (v: string) => onChange(values.filter((x) => x !== v));

  // Up to 8 suggestions that match the current input and aren't already added.
  const matches = useMemo(() => {
    const q = input.trim().toLowerCase();
    return suggestions
      .filter((s) => !values.includes(s) && (q === "" || s.toLowerCase().includes(q)))
      .slice(0, 8);
  }, [suggestions, values, input]);

  return (
    <div className="relative">
      <div
        className="flex flex-wrap items-center gap-1.5 rounded-md border border-neutral-200 bg-white px-2 py-1.5 focus-within:border-blue-400 focus-within:ring-1 focus-within:ring-blue-200"
        onClick={() => inputRef.current?.focus()}
      >
        {values.map((v) => (
          <span
            key={v}
            className={"inline-flex items-center gap-1 rounded-full px-2.5 py-px text-[12px] ring-1 " + chipClass}
          >
            {v}
            <button
              type="button"
              aria-label={`remove ${noun} ${v}`}
              onClick={(e) => {
                e.stopPropagation();
                remove(v);
              }}
              className="text-current/60 hover:text-current"
            >
              ×
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onFocus={() => setFocused(true)}
          onBlur={() => {
            // Commit any pending text so it isn't lost when the user clicks
            // straight to Upload without pressing Enter. (Clicking a
            // suggestion preventDefaults its mousedown, so focus stays and
            // this doesn't fire — no double-add.)
            if (input.trim()) add(input);
            setTimeout(() => setFocused(false), 120);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add(input);
            } else if (e.key === "Backspace" && input === "" && values.length > 0) {
              remove(values[values.length - 1]);
            }
          }}
          placeholder={values.length === 0 ? placeholder : ""}
          className="min-w-[6rem] flex-1 bg-transparent text-[13px] text-neutral-900 placeholder:text-neutral-400 focus:outline-none"
        />
      </div>
      {focused && matches.length > 0 ? (
        <ul className="absolute z-10 mt-1 max-h-44 w-full overflow-auto rounded-md border border-neutral-200 bg-white py-1 shadow-lg">
          {matches.map((m) => (
            <li key={m}>
              <button
                type="button"
                // onMouseDown (not onClick) so it fires before the input's blur.
                onMouseDown={(e) => {
                  e.preventDefault();
                  add(m);
                }}
                className="block w-full px-3 py-1 text-left text-[13px] text-neutral-700 hover:bg-neutral-50"
              >
                {m}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
