# Contributing

Thanks for your interest in arti. Please read this before opening anything.

## This is a release-only repository

This repository is published in **periodic batches** from AngelList's internal
source tree. It is not a development trunk:

- **External pull requests are not reviewed or accepted.** All changes —
  including fixes intended for public users — are developed internally and
  arrive here in the next public batch. A PR opened against this repository
  will be closed without review.
- **Issues and discussions are disabled.** There is no public support
  commitment for this project.
- Public history records public releases; it does not mirror internal commits.

## Forks are the supported customization model

You are welcome — and encouraged — to fork this repository and maintain your
own changes. The tree is deliberately kept self-contained so a fork builds and
tests from a clean machine:

```sh
make setup && make dev-up
make migrate migrate-test
make lint && make test
cd web && npm ci && npm test && npm run build
```

When a new public batch lands, rebase or merge your fork onto the new release
tag; batches are single commits, so the diff between releases is reviewable.

## Security reports

Do **not** open a public issue or PR for a security problem — see
[SECURITY.md](SECURITY.md) for the private reporting path.
