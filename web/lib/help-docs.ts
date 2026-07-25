import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { splitFrontmatter } from "./markdown";

export type HelpDoc = { section: string; slug: string; title: string; order: number; relPath: string };
export type HelpSection = { dir: string; label: string; docs: HelpDoc[] };

// Section order + display labels. Top-level folders under web/docs/.
export const SECTIONS: { dir: string; label: string }[] = [
  { dir: "overview", label: "Overview" },
  { dir: "architecture", label: "Architecture" },
  { dir: "guides", label: "Guides" },
  { dir: "recipes", label: "Recipes" },
  { dir: "reference", label: "Reference" },
  { dir: "operations", label: "Operations" },
  { dir: "development", label: "Development" },
];

// web/docs lives next to this file's package root (process.cwd() === web/ at build).
export const HELP_ROOT = resolve(process.cwd(), "docs");

function parseFrontmatterFields(fm: string | null): { title?: string; order?: number } {
  if (!fm) return {};
  const out: { title?: string; order?: number } = {};
  for (const line of fm.split(/\r?\n/)) {
    const t = /^title:\s*(.+)\s*$/.exec(line);
    if (t) out.title = t[1].trim().replace(/^["']|["']$/g, "");
    const o = /^order:\s*(\d+)\s*$/.exec(line);
    if (o) out.order = parseInt(o[1], 10);
  }
  return out;
}

function firstH1(body: string): string | null {
  const m = /^#\s+(.+)\s*$/m.exec(body);
  return m ? m[1].trim() : null;
}

function readDoc(root: string, dir: string, file: string): HelpDoc {
  const relPath = `${dir}/${file}`;
  const src = readFileSync(join(root, relPath), "utf8");
  const { frontmatter, body } = splitFrontmatter(src);
  const fm = parseFrontmatterFields(frontmatter);
  const base = file.replace(/\.md$/, "");
  const title = fm.title ?? firstH1(body) ?? base;
  return { section: dir, slug: `${dir}/${base}`, title, order: fm.order ?? Number.MAX_SAFE_INTEGER, relPath };
}

export function loadHelpManifest(root: string = HELP_ROOT): HelpSection[] {
  const sections: HelpSection[] = [];
  for (const { dir, label } of SECTIONS) {
    const abs = join(root, dir);
    if (!existsSync(abs)) continue;
    const docs = readdirSync(abs)
      .filter((f) => f.endsWith(".md"))
      .map((f) => readDoc(root, dir, f))
      .sort((a, b) => a.order - b.order || a.title.localeCompare(b.title));
    if (docs.length) sections.push({ dir, label, docs });
  }
  return sections;
}

export function loadHelpDoc(slug: string, root: string = HELP_ROOT): { doc: HelpDoc; markdown: string } | null {
  for (const section of loadHelpManifest(root)) {
    const doc = section.docs.find((d) => d.slug === slug);
    if (doc) return { doc, markdown: readFileSync(join(root, doc.relPath), "utf8") };
  }
  return null;
}

// Serializable nav tree (no fs types) for client components.
export type HelpNav = { dir: string; label: string; docs: { slug: string; title: string }[] }[];
export function helpNav(root: string = HELP_ROOT): HelpNav {
  return loadHelpManifest(root).map((s) => ({
    dir: s.dir,
    label: s.label,
    docs: s.docs.map((d) => ({ slug: d.slug, title: d.title })),
  }));
}
