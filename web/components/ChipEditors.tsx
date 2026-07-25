"use client";

import { useEffect, useRef, useState } from "react";

import { getAggregates, updateArtifactLabels, updateArtifactScopes } from "@/lib/arti";

// The label / scope chip controls, extracted from ArtifactViewer so the "New
// artifact" page presents metadata exactly as the viewer does: bare chips plus a
// "+" affordance, with no input-box chrome around them.
//
// ChipEditor is persistence-agnostic — it reports the next value through
// `onSave`. LabelEditor / ScopeEditor bind that to a PATCH for an existing
// artifact; DraftLabelEditor / DraftScopeEditor bind it to local draft state for
// an artifact that doesn't exist yet.

// ChipEditor — the shared add/remove/typeahead chip control used by both
// labels and scopes. Read-only chips for everyone; creator/admin gets a
// hover-X per chip and a "+" that opens an input with typeahead suggestions
// fetched lazily on first open. Parameterised by colour, the filter-token
// prefix, the suggestion source, and the save fn.
function ChipEditor({
  values,
  canEdit,
  onSave,
  fetchSuggestions,
  tokenPrefix,
  noun,
  chipClass,
  linkClass,
  addClass,
  alwaysLabelAdd = false,
}: {
  values: string[];
  canEdit: boolean;
  onSave: (next: string[]) => Promise<void>;
  fetchSuggestions: () => Promise<string[]>;
  tokenPrefix: "label" | "scope";
  noun: string;
  chipClass: string;
  linkClass: string;
  addClass: string;
  // Keep the noun visible on the "+" instead of revealing it on hover. The
  // viewer's chips usually sit beside existing ones that supply context; a blank
  // create form has none, so two bare "+" buttons would be indistinguishable.
  alwaysLabelAdd?: boolean;
}) {
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>("");
  const [all, setAll] = useState<string[] | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!adding) return;
    inputRef.current?.focus();
    if (all !== null) return;
    fetchSuggestions()
      .then((s) => setAll(s))
      .catch(() => setAll([]));
  }, [adding, all, fetchSuggestions]);

  const save = async (next: string[]) => {
    setBusy(true);
    setErr("");
    try {
      await onSave(next);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = (v: string) => save(values.filter((x) => x !== v));
  const add = (v: string) => {
    const t = v.trim();
    if (!t) return;
    if (values.includes(t)) {
      setAdding(false);
      setDraft("");
      return;
    }
    void save([...values, t]).then(() => {
      setAdding(false);
      setDraft("");
    });
  };

  const q = draft.trim().toLowerCase();
  const suggestions = (all ?? [])
    .filter((v) => !values.includes(v) && (q === "" || v.toLowerCase().includes(q)))
    .slice(0, 8);

  return (
    <>
      {values.map((v) => (
        <span
          key={v}
          className={"group inline-flex items-center rounded-full px-2.5 py-px ring-1 transition " + chipClass}
        >
          <a
            href={`/?q=${encodeURIComponent(tokenPrefix + ":" + v)}`}
            className={"no-underline " + linkClass}
            title={`filter by this ${noun}`}
          >
            {v}
          </a>
          {canEdit ? (
            <button
              type="button"
              onClick={() => remove(v)}
              disabled={busy}
              aria-label={`remove ${noun} ${v}`}
              title="remove"
              className="ml-1 hidden text-neutral-500 hover:text-rose-600 disabled:opacity-50 group-hover:inline"
            >
              ×
            </button>
          ) : null}
        </span>
      ))}
      {canEdit ? (
        adding ? (
          <span className="relative inline-flex items-center">
            <input
              ref={inputRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  add(draft);
                } else if (e.key === "Escape") {
                  setAdding(false);
                  setDraft("");
                }
              }}
              onBlur={() => {
                setTimeout(() => {
                  setAdding(false);
                  setDraft("");
                }, 150);
              }}
              placeholder={noun}
              disabled={busy}
              className="w-40 rounded-full border border-blue-300 bg-white px-2.5 py-px text-[11px] text-neutral-900 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
            />
            {suggestions.length > 0 ? (
              <ul
                role="listbox"
                className="absolute left-0 top-full z-30 mt-1 max-h-56 w-56 overflow-auto rounded-md border border-neutral-200 bg-white py-1 font-sans text-[12px] shadow-lg"
              >
                {suggestions.map((s) => (
                  <li key={s}>
                    <button
                      type="button"
                      // onMouseDown (not onClick) so it fires before the
                      // input's onBlur tears down the dropdown.
                      onMouseDown={(e) => {
                        e.preventDefault();
                        add(s);
                      }}
                      className="block w-full truncate px-2 py-1 text-left text-neutral-700 hover:bg-neutral-100"
                    >
                      {s}
                    </button>
                  </li>
                ))}
              </ul>
            ) : null}
          </span>
        ) : (
          <button
            type="button"
            onClick={() => setAdding(true)}
            disabled={busy}
            aria-label={`add ${noun}`}
            title={`add ${noun}`}
            className={"group rounded-full bg-white px-2 py-px ring-1 transition disabled:opacity-50 " + addClass}
          >
            +<span className={alwaysLabelAdd ? "" : "hidden group-hover:inline"}>{" " + noun}</span>
          </button>
        )
      ) : null}
      {err ? <span className="text-[11px] text-rose-600">error: {err}</span> : null}
    </>
  );
}

export function LabelEditor({
  artifactID,
  labels,
  canEdit,
  onSaved,
}: {
  artifactID: string;
  labels: string[];
  canEdit: boolean;
  onSaved: () => void;
}) {
  return (
    <ChipEditor
      values={labels}
      canEdit={canEdit}
      onSave={async (next) => {
        await updateArtifactLabels(artifactID, next);
        onSaved();
      }}
      fetchSuggestions={async () => (await getAggregates()).labels.map((l) => l.label)}
      tokenPrefix="label"
      noun="label"
      chipClass="bg-neutral-100 ring-neutral-200 hover:bg-neutral-200 text-neutral-700"
      linkClass="text-neutral-700"
      addClass="text-neutral-500 ring-neutral-200 hover:bg-neutral-100 hover:text-neutral-800"
    />
  );
}

export function ScopeEditor({
  artifactID,
  scopes,
  canEdit,
  onSaved,
}: {
  artifactID: string;
  scopes: string[];
  canEdit: boolean;
  onSaved: () => void;
}) {
  return (
    <ChipEditor
      values={scopes}
      canEdit={canEdit}
      onSave={async (next) => {
        await updateArtifactScopes(artifactID, next);
        onSaved();
      }}
      fetchSuggestions={async () => (await getAggregates()).scopes.map((s) => s.scope)}
      tokenPrefix="scope"
      noun="scope"
      chipClass="bg-purple-50 ring-purple-200 hover:bg-purple-100 text-purple-800"
      linkClass="text-purple-800"
      addClass="text-purple-800 ring-purple-200 hover:bg-purple-50"
    />
  );
}

// Draft variants for the "New artifact" page: same chips, same "+", but the
// value is lifted into the caller's draft instead of PATCHed. Nothing here
// commits — the page's single Create button does.
export function DraftLabelEditor({
  labels,
  onChange,
}: {
  labels: string[];
  onChange: (next: string[]) => void;
}) {
  return (
    <ChipEditor
      values={labels}
      canEdit
      onSave={async (next) => onChange(next)}
      fetchSuggestions={async () => (await getAggregates()).labels.map((l) => l.label)}
      tokenPrefix="label"
      noun="label"
      alwaysLabelAdd
      chipClass="bg-neutral-100 ring-neutral-200 hover:bg-neutral-200 text-neutral-700"
      linkClass="text-neutral-700"
      addClass="text-neutral-500 ring-neutral-200 hover:bg-neutral-100 hover:text-neutral-800"
    />
  );
}

export function DraftScopeEditor({
  scopes,
  onChange,
}: {
  scopes: string[];
  onChange: (next: string[]) => void;
}) {
  return (
    <ChipEditor
      values={scopes}
      canEdit
      onSave={async (next) => onChange(next)}
      fetchSuggestions={async () => (await getAggregates()).scopes.map((s) => s.scope)}
      tokenPrefix="scope"
      noun="scope"
      alwaysLabelAdd
      chipClass="bg-purple-50 ring-purple-200 hover:bg-purple-100 text-purple-800"
      linkClass="text-purple-800"
      addClass="text-purple-800 ring-purple-200 hover:bg-purple-50"
    />
  );
}
