"use client";

import DOMPurify from "isomorphic-dompurify";

const PLACEHOLDER_SELECTOR =
  ".mermaid[data-mermaid-placeholder]:not([data-mermaid-rendered]):not([data-mermaid-inflight])";
let renderSequence = 0;
let mermaidModulePromise: Promise<typeof import("mermaid").default> | null = null;

function sourceHash(source: string): string {
  let hash = 0;
  for (let i = 0; i < source.length; i += 1) hash = (hash * 31 + source.charCodeAt(i)) >>> 0;
  return hash.toString(36);
}

function loadMermaid(): Promise<typeof import("mermaid").default> {
  if (!mermaidModulePromise) {
    mermaidModulePromise = import("mermaid")
      .then(({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          suppressErrorRendering: true,
          htmlLabels: false,
          theme: "base",
          themeVariables: {
            fontFamily: "ui-sans-serif, system-ui, sans-serif",
            primaryColor: "#f5f5f5",
            primaryTextColor: "#171717",
            primaryBorderColor: "#a3a3a3",
            lineColor: "#737373",
          },
        });
        return mermaid;
      })
      .catch((error) => {
        mermaidModulePromise = null;
        throw error;
      });
  }
  return mermaidModulePromise;
}

export async function renderMermaidIn(container: HTMLElement): Promise<void> {
  const blocks = Array.from(container.querySelectorAll<HTMLElement>(PLACEHOLDER_SELECTOR)).filter((block) => {
    const source = block.querySelector("pre")?.textContent ?? "";
    return block.dataset.mermaidAttempted !== sourceHash(source);
  });
  if (blocks.length === 0) return;

  blocks.forEach((block) => { block.dataset.mermaidInflight = "true"; });
  let mermaid: typeof import("mermaid").default;
  try {
    mermaid = await loadMermaid();
  } catch {
    blocks.forEach((block) => { delete block.dataset.mermaidInflight; });
    return;
  }

  await Promise.all(
    blocks.map(async (block) => {
      const source = block.querySelector("pre")?.textContent ?? "";
      const hash = sourceHash(source);
      const id = `arti-mermaid-${++renderSequence}`;
      const cleanupRenderNodes = () => {
        document.getElementById(`d${id}`)?.remove();
        document.getElementById(id)?.remove();
      };
      try {
        const { svg } = await mermaid.render(id, source);
        cleanupRenderNodes();
        const sanitized = DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } });
        block.innerHTML = sanitized;
        delete block.dataset.mermaidInflight;
        block.dataset.mermaidRendered = "true";
      } catch (error) {
        cleanupRenderNodes();
        delete block.dataset.mermaidInflight;
        block.dataset.mermaidAttempted = hash;
        const message = error instanceof Error ? error.message : "Invalid Mermaid diagram";
        const errorEl = document.createElement("span");
        errorEl.className = "mt-1 block text-[11px] text-red-600";
        errorEl.textContent = `Diagram could not be rendered: ${message}`;
        block.querySelector("[data-mermaid-error]")?.remove();
        errorEl.dataset.mermaidError = "true";
        block.append(errorEl);
      }
    }),
  );
}
