"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { MIN_PEOPLE_QUERY, searchPeople } from "./arti";

// usePeopleSearch debounces a partial address against GET /api/people and
// returns the matching emails, minus any the caller already has.
//
// Shared by the access editor and the group-membership editor so the two
// cannot drift in the two things that are easy to get wrong here:
//
//   - Out-of-order responses. Two searches in flight and the slower one landing
//     last leaves the list describing a query the user has already typed past.
//     Only the newest request may write state.
//   - Stale results for a DIFFERENT query. Between a keystroke and the response
//     that answers it, the previous query's people are no longer an answer to
//     anything on screen — offering "alice@" to someone who has since typed
//     "bo" is one Enter away from granting the wrong person. Results are
//     therefore tagged with the query that produced them and returned only
//     while that tag still matches.
//   - Failure. A directory lookup that 500s must not break the field it
//     decorates — the input keeps working with no suggestions, which is exactly
//     where it was before the typeahead existed.
export function usePeopleSearch(query: string, exclude: string[] = []): string[] {
  // Query and results are ONE piece of state, not two: held separately there is
  // a window, one render wide, in which the query has advanced and the list has
  // not — and that window is exactly when the list is wrong.
  const [result, setResult] = useState<{ q: string; people: string[] }>({ q: "", people: [] });
  const seq = useRef(0);
  const q = query.trim();
  const tooShort = q.length < MIN_PEOPLE_QUERY;

  useEffect(() => {
    // Bump the sequence even when not searching, so a response already in
    // flight for a longer query cannot land after the field has been cleared
    // and repopulate a list the user just emptied. Nothing is set here: the
    // short-query result is DERIVED below rather than written to state, which
    // keeps this effect free of the synchronous setState that turns one
    // keystroke into a second render pass.
    const mine = ++seq.current;
    if (tooShort) return;
    const t = setTimeout(() => {
      searchPeople(q)
        .then((res) => {
          if (mine === seq.current) setResult({ q, people: res });
        })
        .catch(() => {
          if (mine === seq.current) setResult({ q, people: [] });
        });
    }, 160);
    return () => clearTimeout(t);
  }, [q, tooShort]);

  // Memoized on the exclude list's CONTENT, not its identity: callers build
  // that array inline, so an identity dependency would return a fresh array on
  // every render — and consumers put this value in their own useMemo deps,
  // where a new identity each render defeats the memo it is feeding.
  const excludeKey = exclude.join(",").toLowerCase();
  return useMemo(() => {
    // Derived, not stored: below the floor — or while the newest query is still
    // unanswered — the answer is "nothing", whatever the last completed search
    // returned.
    if (tooShort || result.q !== q) return [];
    const have = new Set(excludeKey.split(",").filter(Boolean));
    return result.people.filter((e) => !have.has(e.toLowerCase()));
  }, [result, q, excludeKey, tooShort]);
}
