import { describe, expect, it, vi } from "vitest";
import { handleAppConsentMessage } from "./useAppConsentMessage";

const APP = "0b6f2e9e-9d1a-4b1c-8f1e-1d2c3b4a5f60";

describe("handleAppConsentMessage", () => {
  it("records consent then reloads the frame", async () => {
    const contentWindow = {};
    const frame = { contentWindow, src: `/api/artifacts/${APP}/files/index.html` } as HTMLIFrameElement;
    const post = vi.fn().mockResolvedValue({ ok: true } as Response);

    const handled = await handleAppConsentMessage(
      { source: contentWindow, data: { source: "arti-app-consent", appId: APP } } as MessageEvent,
      frame,
      post,
    );

    expect(handled).toBe(true);
    expect(post).toHaveBeenCalledWith(`/app/${APP}/consent`, "");
  });

  it("forwards the all-versions scope and nothing else", async () => {
    const contentWindow = {};
    const frame = { contentWindow, src: `/api/artifacts/${APP}/files/index.html` } as HTMLIFrameElement;
    const post = vi.fn().mockResolvedValue({ ok: true } as Response);
    await handleAppConsentMessage(
      { source: contentWindow, data: { source: "arti-app-consent", appId: APP, scope: "slug" } } as MessageEvent,
      frame,
      post,
    );
    expect(post).toHaveBeenCalledWith(`/app/${APP}/consent`, "slug");
    await handleAppConsentMessage(
      { source: contentWindow, data: { source: "arti-app-consent", appId: APP, scope: "everything" } } as MessageEvent,
      frame,
      post,
    );
    expect(post).toHaveBeenLastCalledWith(`/app/${APP}/consent`, "");
  });

  const OTHER = "11111111-2222-4333-8444-555555555555";

  it.each([
    ["wrong frame", {}, { source: "arti-app-consent", appId: APP }, `/api/artifacts/${APP}/files/index.html`],
    ["wrong source field", null, { source: "arti-open-external", appId: APP }, `/api/artifacts/${APP}/files/index.html`],
    ["non-uuid appId", null, { source: "arti-app-consent", appId: "../admin" }, `/api/artifacts/${APP}/files/index.html`],
    // A framed document naming a different app than the parent loaded: the
    // frame may have navigated itself, or be a package page posting on its own.
    ["appId that is not the framed app", null, { source: "arti-app-consent", appId: OTHER }, `/api/artifacts/${APP}/files/index.html`],
    ["frame with no artifact src", null, { source: "arti-app-consent", appId: APP }, ""],
  ])("ignores %s", async (_name, otherSource, data, src) => {
    const contentWindow = {};
    const frame = { contentWindow, src } as HTMLIFrameElement;
    const post = vi.fn();

    const handled = await handleAppConsentMessage(
      { source: otherSource ?? contentWindow, data } as MessageEvent,
      frame,
      post,
    );

    expect(handled).toBe(false);
    expect(post).not.toHaveBeenCalled();
  });
});
