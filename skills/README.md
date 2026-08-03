# Agent skills for arti

These are portable [agent skills](https://agentskills.io) that teach an AI
coding assistant how to use **arti** — the artifact store this repository builds.
Point your assistant at this directory (or copy a skill into your assistant's
skills folder) and it will know how to publish, fetch, search, version, and build
artifacts against your arti instance.

| Skill | Use when |
|---|---|
| [`arti-usage-guide`](arti-usage-guide/SKILL.md) | Reaching arti from a **hosted agent** (no local CLI) — via the arti MCP tools, or an API-key bearer + `curl`, or the device flow. |
| [`arti-cli-usage-guide`](arti-cli-usage-guide/SKILL.md) | Reaching arti from a **local machine** with the `arti` command-line binary. |
| [`create-arti-app-artifact`](create-arti-app-artifact/SKILL.md) | **Building an APP artifact** — a sandboxed HTML mini-site that calls MCP tools / an LLM through arti's governed proxy. |

All three are tenant-neutral: they refer to your instance by `$ARTI_BASE_URL`
(or a placeholder like `https://arti.example.com`) and to whoever runs the
service as "the operator". Adapt the endpoint and any org-specific conventions
(labels, allowed origins) to your deployment.
