import { describe, expect, it } from "vitest";
import { credentialLabel, credentialTooltip } from "./credential";

describe("credentialLabel", () => {
  // The incident: an agent holding one person's key wrote hundreds of
  // documents that read as that person's own work. The key's name is the
  // detail that makes them tell apart at a glance.
  it("names the key a document was written with", () => {
    expect(credentialLabel("apikey:3f2a", "cn-angellist")).toBe("cn-angellist");
  });

  it("still says a key was used when the name is missing", () => {
    expect(credentialLabel("apikey:3f2a", null)).toBe("an API key");
  });

  // A chip on every browser upload would be noise, and noise is what stops
  // people reading the chip that matters.
  it("shows nothing for a browser session", () => {
    expect(credentialLabel("session", null)).toBe("");
  });

  // Absent attribution means the document predates it. It must not be
  // presented as a person publishing in a browser.
  it("shows nothing when attribution is absent", () => {
    expect(credentialLabel(null, null)).toBe("");
    expect(credentialTooltip(null)).toBe("");
  });

  it("labels the other credential kinds", () => {
    expect(credentialLabel("device:fam-1", null)).toBe("an upload token");
    expect(credentialLabel("token", null)).toBe("a CLI or MCP token");
    expect(credentialLabel("service", null)).toBe("the service credential");
  });

  // An app writes as whoever is viewing it, so `creator` names a person who
  // may never have typed anything. The app's own name is the missing half.
  it("names the app a document was written by", () => {
    expect(credentialLabel("app:b692fc02", "KYC Eligibility Dashboard")).toBe(
      "KYC Eligibility Dashboard",
    );
    expect(credentialLabel("app:b692fc02", null)).toBe("an arti app");
    expect(credentialTooltip("app:b692fc02")).toContain("on behalf of the person viewing it");
  });

  // An unknown kind from a newer server must show the raw reference rather
  // than silently disappear.
  it("falls back to the raw reference", () => {
    expect(credentialLabel("something-new:1", null)).toBe("something-new:1");
  });

  it("explains that a key's creator is its owner", () => {
    expect(credentialTooltip("apikey:3f2a")).toContain("authenticates as its owner");
  });
});
