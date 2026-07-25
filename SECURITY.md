# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** through GitHub's
security advisory flow for this repository:

**[Report a vulnerability](https://github.com/angellist/arti-oss/security/advisories/new)**

Do not open a public issue, discussion, or pull request for a security
problem, and do not include exploit details in commit messages or forks.

What to include: the affected component (server, web UI, CLI, MCP surface),
a reproduction or proof of concept, and the impact you believe it has.

## What happens next

This repository is published in periodic batches from a private source tree,
so fixes are developed and verified internally and ship in the next public
batch (expedited for serious issues). We'll acknowledge your report, keep you
updated through the advisory, and credit you in the advisory if you'd like.

## Scope notes for self-hosters

arti's security posture assumes the deployment guidance in the README and
`web/docs/guides/self-hosting.md` is followed — in particular:

- `ARTI_AUTH_MODE=disabled` is for local development only and must never be
  network-reachable.
- `proxy` mode is safe **only** when arti is unreachable except through the
  authenticating reverse proxy.
- An empty domain allowlist admits nobody by design; misconfiguration reports
  that amount to opting out of these guardrails are not vulnerabilities.
