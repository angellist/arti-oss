// Display rules for the credential that wrote a document
// (ArtifactInfo.written_via / written_via_name).

// credentialLabel turns a stored credential reference into what a reader
// should see. A browser session returns "" — `creator` already says a person
// published it, and a "via session" chip on every document would bury the
// cases that matter. Everything else is worth naming: those are the writes a
// person did not necessarily make themselves.
export function credentialLabel(
  writtenVia: string | null,
  writtenViaName: string | null,
): string {
  if (!writtenVia) return "";
  const [kind] = writtenVia.split(":", 1);
  switch (kind) {
    case "session":
      return "";
    case "apikey":
      return writtenViaName || "an API key";
    case "device":
      return "an upload token";
    case "token":
      return "a CLI or MCP token";
    case "service":
      return "the service credential";
    case "app":
      return writtenViaName || "an arti app";
    default:
      return writtenVia;
  }
}

// credentialTooltip explains what the label means, including for a document
// written before attribution existed.
export function credentialTooltip(writtenVia: string | null): string {
  if (!writtenVia) return "";
  const [kind] = writtenVia.split(":", 1);
  switch (kind) {
    case "apikey":
      return "Written with an API key. The key authenticates as its owner, so the creator above is the key's owner — not necessarily who ran it.";
    case "device":
      return "Written with an upload token issued to a headless agent.";
    case "token":
      return "Written with a user bearer token (the arti CLI, or an MCP client).";
    case "service":
      return "Written with arti's shared service credential.";
    case "app":
      return "Written by an arti app, on behalf of the person viewing it. The creator above is that viewer.";
    default:
      return `Written via ${writtenVia}.`;
  }
}
