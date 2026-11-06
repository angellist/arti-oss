// Renders a ```diff fenced block as HTML in a chat-style diff view: a header
// with the file and +/− counts, a gutter, and long unchanged runs folded into
// a <details>. Accepts a real unified diff and a hand-written one: a line with
// no +/-/space prefix is an unchanged context line.

type DiffRowKind = "add" | "del" | "ctx" | "hunk" | "meta" | "file";

export interface DiffRow {
  kind: DiffRowKind;
  text: string;
}

interface ParsedDiff {
  file?: string;
  files: number;
  rows: DiffRow[];
  added: number;
  removed: number;
}

type DiffSegment = { kind: "row"; row: DiffRow } | { kind: "fold"; rows: DiffRow[] };

const GIT_META = /^(diff --git |index [0-9a-f]+\.\.|new file mode |deleted file mode |similarity index |rename (from|to) )/;

function headerPath(line: string): string | undefined {
  const p = line.slice(4).split("\t")[0].trim();
  if (!p || p === "/dev/null") return undefined;
  return p.replace(/^[ab]\//, "");
}

export function parseDiff(src: string): ParsedDiff {
  const lines = src.replace(/\n$/, "").split("\n");
  const rows: DiffRow[] = [];
  let file: string | undefined;
  let files = 0;
  let added = 0;
  let removed = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // `---`/`+++` is a file header only as a pair; alone it is a changed line.
    if (line.startsWith("--- ") && lines[i + 1]?.startsWith("+++ ")) {
      const path = headerPath(lines[i + 1]) ?? headerPath(line) ?? "";
      files++;
      // From the second file on, a row marks where each file starts.
      if (files === 2) rows.unshift({ kind: "file", text: file ?? "" });
      if (files >= 2) rows.push({ kind: "file", text: path });
      file ??= path || undefined;
      i++;
      continue;
    }
    if (GIT_META.test(line)) continue;
    if (line.startsWith("@@")) {
      const m = /^@@[^@]*@@\s?(.*)$/.exec(line);
      rows.push({ kind: "hunk", text: m ? m[1] : line });
    } else if (line.startsWith("+")) {
      rows.push({ kind: "add", text: line.slice(1) });
      added++;
    } else if (line.startsWith("-")) {
      rows.push({ kind: "del", text: line.slice(1) });
      removed++;
    } else if (line.startsWith("\\ ")) {
      rows.push({ kind: "meta", text: line.slice(2) });
    } else {
      rows.push({ kind: "ctx", text: line.startsWith(" ") ? line.slice(1) : line });
    }
  }
  return { file: files > 1 ? `${files} files` : file, files, rows, added, removed };
}

// Collapses long runs of unchanged lines, keeping `keep` lines of context on
// each side of every change. Short diffs are returned unfolded.
export function foldContext(rows: DiffRow[], opts: { minTotal?: number; keep?: number } = {}): DiffSegment[] {
  const minTotal = opts.minTotal ?? 40;
  const keep = opts.keep ?? 3;
  const asRows = rows.map((row): DiffSegment => ({ kind: "row", row }));
  if (rows.length <= minTotal) return asRows;
  const out: DiffSegment[] = [];
  let i = 0;
  while (i < rows.length) {
    if (rows[i].kind !== "ctx") {
      out.push(asRows[i++]);
      continue;
    }
    let j = i;
    while (j < rows.length && rows[j].kind === "ctx") j++;
    const head = i === 0 ? 0 : keep;
    const tail = j === rows.length ? 0 : keep;
    // Folding fewer than 4 lines saves less space than the fold row costs.
    if (j - i - head - tail >= 4) {
      out.push(...asRows.slice(i, i + head));
      out.push({ kind: "fold", rows: rows.slice(i + head, j - tail) });
      out.push(...asRows.slice(j - tail, j));
    } else {
      out.push(...asRows.slice(i, j));
    }
    i = j;
  }
  return out;
}

function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// The +/− marks are drawn by CSS, so selecting and copying a block yields clean text.
// A context row carries its text in data-t too: side by side shows it again on the
// right through ::after, which adds nothing to the page text the comments overlay reads.
function rowHtml(r: DiffRow): string {
  const t = esc(r.text);
  const cls = r.kind === "file" ? "file-row" : r.kind;
  const data = r.kind === "ctx" ? ` data-t="${t}"` : "";
  return `<div class="arti-diff-row arti-diff-${cls}"${data}><span class="arti-diff-gutter" aria-hidden="true"></span><span class="arti-diff-text">${t}</span></div>`;
}

// Wraps each run of removed lines and the added lines after it in one group, which
// side by side lays out as two columns. Unified order is unchanged.
function rowsHtml(rows: DiffRow[]): string {
  let out = "";
  for (let i = 0; i < rows.length; ) {
    if (rows[i].kind !== "del" && rows[i].kind !== "add") {
      out += rowHtml(rows[i++]);
      continue;
    }
    let group = "";
    while (rows[i]?.kind === "del") group += rowHtml(rows[i++]);
    while (rows[i]?.kind === "add") group += rowHtml(rows[i++]);
    out += `<div class="arti-diff-chg">${group}</div>`;
  }
  return out;
}

const TOGGLE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" aria-hidden="true">' +
  '<rect x="1.5" y="2.5" width="13" height="11" rx="1.6"></rect>' +
  '<path class="arti-diff-icon-unified" d="M4 5.5h8M4 8h6M4 10.5h7"></path>' +
  '<path class="arti-diff-icon-split" d="M8 2.5v11M3.6 5.5h2.6M3.6 8h2M3.6 10.5h2.4M9.8 5.5h2.6M9.8 8h2M9.8 10.5h2.4"></path></svg>';

export function renderDiffBlock(src: string): string {
  const diff = parseDiff(src);
  const segments = foldContext(diff.rows);
  let body = "";
  let run: DiffRow[] = [];
  for (const s of segments) {
    if (s.kind === "row") {
      run.push(s.row);
      continue;
    }
    body += rowsHtml(run);
    run = [];
    body += `<details class="arti-diff-fold"><summary>⋯ ${s.rows.length} unchanged line${s.rows.length === 1 ? "" : "s"}</summary>${rowsHtml(s.rows)}</details>`;
  }
  body += rowsHtml(run);
  const file = esc(diff.file ?? "diff");
  return (
    `<div class="arti-diff not-prose" role="figure" aria-label="diff${diff.file ? ` of ${file}` : ""}">` +
    `<div class="arti-diff-head">` +
    `<button type="button" class="arti-diff-toggle" data-arti-diff-toggle aria-label="Toggle side-by-side view" title="Side by side / unified">${TOGGLE_ICON}</button>` +
    `<span class="arti-diff-file">${file}</span>` +
    `<span class="arti-diff-count arti-diff-count-add">+${diff.added}</span>` +
    `<span class="arti-diff-count arti-diff-count-del">−${diff.removed}</span></div>` +
    `<div class="arti-diff-body">${body}</div></div>\n`
  );
}
