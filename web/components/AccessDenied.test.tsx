// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";

import AccessDenied from "./AccessDenied";
import { DenialInfo } from "@/lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const denial: DenialInfo = {
  named_slug: "al-service-graph-explorer",
  version: 4,
  title: "Service Dependency Graph",
  owner: "owner@example.com",
};

function render(info: DenialInfo) {
  act(() => root.render(<AccessDenied info={info} />));
}

// The page replaces a 404 that sent readers back to whoever shared the link.
// If any of these three facts is missing it has not replaced anything.
it("names the document, its slug and the person who can grant access", () => {
  render(denial);
  const text = container.textContent ?? "";
  expect(text).toContain("Service Dependency Graph");
  expect(text).toContain("al-service-graph-explorer");
  expect(text).toContain("owner@example.com");
});

it("offers a prefilled request addressed to the owner", () => {
  render(denial);
  const link = container.querySelector<HTMLAnchorElement>('a[href^="mailto:"]');
  expect(link).not.toBeNull();
  expect(link!.getAttribute("href")).toContain("mailto:owner%40example.com");
  expect(decodeURIComponent(link!.getAttribute("href") ?? "")).toContain("Service Dependency Graph");
});

// A slugless artifact is reached only by /a/<uuid>; the page still has to
// render rather than print "null" where the slug goes.
it("renders without a slug", () => {
  render({ ...denial, named_slug: null });
  const text = container.textContent ?? "";
  expect(text).toContain("Service Dependency Graph");
  expect(text).not.toContain("null");
});
