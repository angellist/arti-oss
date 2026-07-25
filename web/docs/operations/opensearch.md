---
title: OpenSearch
order: 4
summary: How to deploy, backfill, and operate the optional OpenSearch full-text search layer — locally and in a real deployment.
---

# OpenSearch

arti uses an OpenSearch index (`artifacts`) for full-text BM25 search across
titles, descriptions, creator names, labels, and artifact content. The search
layer is **optional** — when `OPENSEARCH_ENDPOINT` is empty the server falls
back to Postgres `ILIKE` queries. This makes rollout incremental and
risk-free.

## Architecture overview

```
┌────────────┐      sync-on-write       ┌──────────────────┐
│  Postgres  │ ─────────────────────────▶│   OpenSearch      │
│  (source)  │                           │  index: artifacts │
└────────────┘                           └──────────────────┘
       ▲                                          │
       │  CanAccess re-check                      │ BM25 search
       │  (defense-in-depth)                      │ + highlights
       └──────────────────── arti-server ◀────────┘
```

Writes flow through Postgres first, then a fire-and-forget goroutine indexes
the document into OpenSearch. Reads query OpenSearch for relevance-ranked IDs,
then fetch full rows from Postgres and re-verify access controls.

## Environment variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `OPENSEARCH_ENDPOINT` | No | `""` | Full URL (e.g. `https://search-arti-xxx.us-west-2.es.amazonaws.com`). Empty = disabled. |
| `OPENSEARCH_USERNAME` | No | `""` | Basic-auth username (internal user DB or master user). |
| `OPENSEARCH_PASSWORD` | No | `""` | Basic-auth password. |

In Kubernetes, source these from a Secret; in compose, from the environment.

---

## Local development

The docker-compose stack includes a single-node OpenSearch 2.19 container with
security disabled:

```sh
make dev-up
# Starts: Postgres :5436, MinIO :9210, OpenSearch :9200
```

The server auto-connects when you set the endpoint:

```sh
export OPENSEARCH_ENDPOINT=http://localhost:9200
# (no username/password needed — security plugin is disabled locally)
```

Verify the cluster is healthy:

```sh
curl -s http://localhost:9200/_cluster/health | jq .status
# → "green"
```

On server start (`arti-server serve`), `EnsureIndex()` creates the `artifacts`
index with the correct mapping if it doesn't exist.

---

## Deploying

To roll OpenSearch out to a real deployment:

### 1. Provision an OpenSearch endpoint (one-time)

Anything OpenSearch-2.x-compatible works: a managed AWS OpenSearch domain, a
self-managed cluster, or a single container for small installs. A modest
instance (e.g. 1× `t3.small.search`, 20 GiB) comfortably handles thousands of
artifacts — see the [cost estimate](#cost-estimate) below.

### 2. Point the server at it

Deploy arti with `OPENSEARCH_ENDPOINT` (and `OPENSEARCH_USERNAME` /
`OPENSEARCH_PASSWORD` when basic auth is enabled) set. On boot,
`EnsureIndex()` creates the `artifacts` index with the correct mapping if it
doesn't exist. Nothing else changes — the search endpoint transparently
upgrades from Postgres `ILIKE` to BM25.

### 3. Backfill the index

The `reindex` command pages through all Postgres artifacts and indexes them
into OpenSearch. Run it once wherever the server image runs — exec into the
running pod/container, or run a one-shot Job with the same
`ARTI_DATABASE_URL`, `OPENSEARCH_*`, and `S3_*` environment as the server:

```sh
# Exec into the running container:
kubectl -n <namespace> exec -it deploy/<arti-server-deployment> -- /app/arti-server reindex

# Or with compose:
docker compose exec arti /app/arti-server reindex
```

The reindex command:
1. Pages through all artifacts (including archived) in batches of 100
2. Indexes each document with full content extraction
3. Sweeps `is_latest` flags per slug to ensure only the highest live version is marked latest

Progress is logged as JSON:
```json
{"level":"INFO","msg":"reindex progress","indexed":500,"db_total":1234,"elapsed":"12s"}
{"level":"INFO","msg":"reindex complete","total_indexed":1234,"slugs_swept":456,"elapsed":"45s"}
```

### 4. Verify search works

```sh
# From a machine with network access to the arti endpoint:
curl -s "https://arti.example.com/api/artifacts?q=your+search+term" \
  -H "Authorization: Bearer <token>" | jq '.artifacts[].title'

# Or check the index directly:
curl -s "https://<opensearch-endpoint>/artifacts/_count" \
  -u "<username>:<password>" | jq .count
```

In the web UI, type a search query — results should now include content
matches with highlighted snippets.

---

## Operations

### Index health

```sh
# Cluster health
curl -s "$OPENSEARCH_ENDPOINT/_cluster/health" -u "$OPENSEARCH_USERNAME:$OPENSEARCH_PASSWORD" | jq .

# Index stats
curl -s "$OPENSEARCH_ENDPOINT/artifacts/_stats" -u "$OPENSEARCH_USERNAME:$OPENSEARCH_PASSWORD" | jq '.indices.artifacts.primaries.docs'

# Mapping
curl -s "$OPENSEARCH_ENDPOINT/artifacts/_mapping" -u "$OPENSEARCH_USERNAME:$OPENSEARCH_PASSWORD" | jq .
```

### Re-indexing

The `reindex` command is idempotent — safe to re-run at any time. It overwrites
existing documents by ID. Use it after:
- Schema changes to the index mapping (delete the index first to pick up new
  mappings from `EnsureIndex()`)
- Bulk data imports that bypassed the normal write path
- Disaster recovery (OpenSearch data loss)

To delete and recreate the index:
```sh
curl -XDELETE "$OPENSEARCH_ENDPOINT/artifacts" -u "$OPENSEARCH_USERNAME:$OPENSEARCH_PASSWORD"
# Restart the server (or hit any search endpoint) to trigger EnsureIndex()
# Then run reindex
```

### Graceful degradation

If OpenSearch is unreachable or returns errors, the server logs a warning and
falls back to the Postgres search path. No user-facing errors occur — results
will be less relevant (title/description ILIKE only) until OpenSearch recovers.

### Sync-on-write behavior

Every mutating operation (create, append, patch, archive, unarchive) triggers a
background goroutine that indexes the affected document. This means:
- New artifacts are searchable within ~1-2 seconds of creation
- No periodic sync job is needed for normal operation
- If a sync fails (network blip), the document remains stale until the next
  write or a manual reindex

### Access control

OpenSearch stores `allowed_access` patterns per document and applies them as
query filters. As defense-in-depth, the server re-checks `pgstore.CanAccess` on
every row fetched from Postgres after an OpenSearch query — so stale ACLs in the
index never leak data.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Search returns no results | Index empty | Run `reindex` |
| Search returns no results | `OPENSEARCH_ENDPOINT` not set | Check the server's environment/secret wiring |
| Stale results after access change | Sync-on-write didn't fire | Next write to the artifact fixes it; or run `reindex` |
| `is_latest` wrong on old versions | Archived version not cleared | `reindex` sweeps all slugs |
| High latency on search | Undersized instance | Scale up the OpenSearch instance type |
| Index mapping conflict | Schema changed | Delete index, restart server, reindex |

---

## Cost estimate

| Environment | Instance | Storage | Monthly |
|-------------|----------|---------|---------|
| Staging | 1x t3.small.search | 20 GiB gp3 | ~$28 |
| Production | 2x t3.medium.search (multi-AZ) | 50 GiB gp3 | ~$115 |

No data transfer charges for same-VPC access. Costs scale with document count
and query volume; current artifact counts (~low thousands) are well within these
instance sizes.
