import { describe, expect, it } from "vitest";

import { auditRows, usageCredLabel } from "./AccessAuditModal";
import type { CredentialAudit } from "@/lib/types";

const future = "2999-01-01T00:00:00Z";
const past = "2000-01-01T00:00:00Z";

const audit: CredentialAudit = {
  email: "a@example.com",
  api_keys: [
    {
      id: "k1",
      name: "ci",
      key_prefix: "arti_upload_ab",
      scopes: ["upload"],
      created_at: past,
      expires_at: future,
      last_used_at: "2026-01-01T00:00:00Z",
      revoked_at: null,
    },
    {
      id: "k2",
      name: "old",
      key_prefix: "arti_upload_cd",
      scopes: ["upload"],
      created_at: past,
      expires_at: future,
      last_used_at: null,
      revoked_at: past,
    },
  ],
  device_tokens: [
    {
      family_id: "fam-123456789",
      scope: "upload",
      created_at: past,
      rotated_at: past,
      expires_at: past,
      revoked: false,
    },
  ],
  share_links: [],
  connections: [
    { resource: "https://mcp.notion.com/mcp", expiry: past, updated_at: past },
  ],
  usage: [
    {
      cred: "apikey:k1",
      ip: "1.2.3.4",
      user_agent: "curl",
      reads: 1,
      writes: 0,
      first_seen: past,
      last_seen: "2026-02-01T00:00:00Z",
    },
    {
      cred: "token",
      ip: "1.2.3.4",
      user_agent: "runlayer",
      reads: 0,
      writes: 3,
      first_seen: past,
      last_seen: past,
    },
  ],
  usage_days: 90,
};

describe("auditRows", () => {
  const rows = Object.fromEntries(auditRows(audit).map((r) => [r.id, r]));

  it("takes the later of the key's own last-use and its usage rows", () => {
    expect(rows.k1.lastUsed).toBe("2026-02-01T00:00:00Z");
  });

  it("offers revoke only while a credential still works", () => {
    expect(rows.k1.status).toBe("Active");
    expect(rows.k2.status).toBe("Revoked");
    expect(rows["fam-123456789"].status).toBe("Expired");
  });

  // The stored access token expires and refreshes on its own; an expired one
  // is not a dead connection.
  it("keeps a connection active past its access-token expiry", () => {
    const c = rows["https://mcp.notion.com/mcp"];
    expect(c.status).toBe("Active");
    expect(c.revokeLabel).toBe("Disconnect");
  });
});

describe("usageCredLabel", () => {
  it("names an API key by its owner-given name and the unstored kinds plainly", () => {
    expect(usageCredLabel("apikey:k1", audit)).toBe("API key ci");
    expect(usageCredLabel("token", audit)).toBe("CLI / MCP token");
    expect(usageCredLabel("device:fam-123456789", audit)).toBe(
      "upload token fam-1234",
    );
  });
});
