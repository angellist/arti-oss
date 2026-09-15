package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// rowQuerier is the QueryRow surface shared by *pgxpool.Pool and pgx.Tx, so
// owner resolution reads the same way inside Put's slug critical section as it
// does on an ordinary request.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNoOwner reports that a slug has no owner and no versions to derive one
// from — it does not exist.
var ErrNoOwner = errors.New("pgstore: slug has no owner")

// SlugOwner returns the email that owns slug. Ownership is per-DOCUMENT and
// immutable under version churn: pushing a version, archiving v1, or being
// granted write never moves it. Only TransferSlugOwner does.
func (s *Store) SlugOwner(ctx context.Context, slug string) (string, error) {
	return slugOwner(ctx, s.pool, slug)
}

func slugOwner(ctx context.Context, q rowQuerier, slug string) (string, error) {
	owner, found, err := ownerRow(ctx, q, slug)
	if err != nil || found {
		return owner, err
	}
	return derivedSlugOwner(ctx, q, slug)
}

// ownerRow reads the recorded owner without the derivation fallback, so a
// caller that must distinguish "no row yet" from "owner is X" can.
func ownerRow(ctx context.Context, q rowQuerier, slug string) (string, bool, error) {
	var owner string
	err := q.QueryRow(ctx, `SELECT owner_email FROM artifact_owners WHERE named_slug = $1`, slug).Scan(&owner)
	switch {
	case err == nil:
		return owner, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	default:
		return "", false, err
	}
}

// derivedSlugOwner recomputes ownership the way it was defined before
// artifact_owners existed: the creator of the slug's earliest version,
// counting archived ones. Reachable only for a slug written by a binary that
// predates migration 0029 — a rolling deploy serves both for a few minutes —
// and healed by that slug's next write.
func derivedSlugOwner(ctx context.Context, q rowQuerier, slug string) (string, error) {
	var creator string
	err := q.QueryRow(ctx,
		`SELECT creator FROM artifacts WHERE named_slug = $1 ORDER BY version ASC LIMIT 1`,
		slug).Scan(&creator)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoOwner
	}
	return creator, err
}

// DocOwner resolves the owner of any artifact row: the slug's owner, or the
// creator for a slugless artifact, which has no lineage to own. This is the
// single definition — the ACL gate, the comment switch, the share control, the
// denial page and comment notifications all resolve ownership through it.
func (s *Store) DocOwner(ctx context.Context, row sqlc.Artifact) (string, error) {
	if row.NamedSlug == nil || *row.NamedSlug == "" {
		return row.Creator, nil
	}
	owner, err := slugOwner(ctx, s.pool, *row.NamedSlug)
	if errors.Is(err, ErrNoOwner) {
		return row.Creator, nil
	}
	if err != nil {
		return "", err
	}
	return owner, nil
}

// claimSlugOwner records the owner of slug if it has none, and returns the
// owner either way. Must run inside Put's transaction, under the slug advisory
// lock, so the claim commits with the version that justified it.
//
// A slug that already has versions but no owner row predates migration 0029:
// the claim takes its earliest creator, never the caller, so a publish can
// never make the publisher the owner of someone else's document.
//
// Archiving every version frees a slug for reuse but does NOT release its
// ownership: a republish under that name lands under the original owner.
func claimSlugOwner(ctx context.Context, tx pgx.Tx, slug, creator string) (string, error) {
	owner, found, err := ownerRow(ctx, tx, slug)
	if err != nil || found {
		return owner, err
	}
	// No row: either a brand-new slug, whose creator owns it, or one written
	// before migration 0029, whose earliest creator does. Reading through
	// slugOwner here would return the derived answer and never record it.
	owner, err = derivedSlugOwner(ctx, tx, slug)
	switch {
	case errors.Is(err, ErrNoOwner):
		owner = creator
	case err != nil:
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO artifact_owners (named_slug, owner_email) VALUES ($1, $2)
		 ON CONFLICT (named_slug) DO NOTHING`, slug, owner); err != nil {
		return "", fmt.Errorf("pgstore: claim owner: %w", err)
	}
	return slugOwner(ctx, tx, slug)
}

// TransferSlugOwner moves ownership of slug to newOwner and grants them read
// access on every version, so the new owner is never locked out of the
// document they now own. Returns the previous owner.
//
// keepPrev additionally grants the PREVIOUS owner read and write. Without it a
// hand-off silently demotes them: CanWrite answers to the owner and has no
// creator fallback, so the moment ownership moves they lose write on a document
// they may still be working in. Read they keep on versions they created
// (CanAccess does short-circuit on creator), which is why this is a grant and
// never a revocation — the new owner can withdraw it like any other entry.
//
// authorize decides whether the transfer may proceed, and runs against the
// owner read INSIDE this transaction under the slug lock — not one the caller
// looked up earlier. Two transfers racing would otherwise both pass a
// service-layer check and the loser's write would still land, handing the
// document to someone the current owner never chose.
func (s *Store) TransferSlugOwner(ctx context.Context, slug, newOwner, by string, keepPrev bool, authorize func(prev string) error) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("pgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := acquireSlugLock(ctx, tx, slug); err != nil {
		return "", err
	}
	prev, err := slugOwner(ctx, tx, slug)
	if err != nil {
		return "", err
	}
	if authorize != nil {
		if err := authorize(prev); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO artifact_owners (named_slug, owner_email, updated_by) VALUES ($1, $2, $3)
		 ON CONFLICT (named_slug) DO UPDATE
		    SET owner_email = EXCLUDED.owner_email, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		slug, newOwner, by); err != nil {
		return "", fmt.Errorf("pgstore: transfer owner: %w", err)
	}
	if err := grantReadBySlugTx(ctx, tx, slug, newOwner); err != nil {
		return "", err
	}
	if keepPrev && prev != "" && !strings.EqualFold(prev, newOwner) {
		if err := grantReadBySlugTx(ctx, tx, slug, prev); err != nil {
			return "", err
		}
		if err := grantWriteBySlugTx(ctx, tx, slug, prev); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("pgstore: commit: %w", err)
	}
	return prev, nil
}

// grantReadBySlugTx unions one token into allowed_access on every version of
// slug, archived rows included — the same reach as an ACL fan-out, so an
// unarchive cannot resurrect a version the owner cannot read. Rows the
// existing patterns already grant are left untouched, so a `*` or domain-glob
// document gains no redundant token.
func grantReadBySlugTx(ctx context.Context, tx pgx.Tx, slug, token string) error {
	type grant struct {
		id     pgtype.UUID
		access []string
	}
	var pending []grant

	rows, err := tx.Query(ctx,
		`SELECT artifact_id, allowed_access FROM artifacts WHERE named_slug = $1`, slug)
	if err != nil {
		return err
	}
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.id, &g.access); err != nil {
			rows.Close()
			return err
		}
		if !matchPatterns(g.access, token, nil) {
			g.access = unionTokens(g.access, []string{token})
			pending = append(pending, g)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, g := range pending {
		if _, err := tx.Exec(ctx,
			`UPDATE artifacts SET allowed_access = $2, modified_at = now() WHERE artifact_id = $1`,
			g.id, g.access); err != nil {
			return fmt.Errorf("pgstore: grant owner read: %w", err)
		}
	}
	return nil
}

// grantWriteBySlugTx unions one token into allowed_write on every version of
// slug that keeps an EXPLICIT write list, and into allowed_access alongside it.
//
// Both columns, because write ⊆ read is the invariant finalAccess enforces on
// every other ACL write path. Adding to write alone leaves a token that
// allowed_access does not carry, and the access editor builds its rows from
// allowed_access — so the grantee is invisible in the dialog and the next
// Confirm, rebuilding both lists from the visible rows, silently drops their
// write. The read token is appended literally rather than skipped when a
// pattern already matches, since a `*` that covers them for read does not keep
// them in the list the editor renders.
//
// A nil write list is mirror mode, where write follows read and the read grant
// has already carried the token; filling it in would make it explicit and
// sticky for every later version.
func grantWriteBySlugTx(ctx context.Context, tx pgx.Tx, slug, token string) error {
	type grant struct {
		id     pgtype.UUID
		access []string
		write  []string
	}
	var pending []grant

	rows, err := tx.Query(ctx,
		`SELECT artifact_id, allowed_access, allowed_write FROM artifacts
		  WHERE named_slug = $1 AND allowed_write IS NOT NULL`, slug)
	if err != nil {
		return err
	}
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.id, &g.access, &g.write); err != nil {
			rows.Close()
			return err
		}
		if !matchPatterns(g.write, token, nil) {
			g.write = unionTokens(g.write, []string{token})
			g.access = unionTokens(g.access, []string{token})
			pending = append(pending, g)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, g := range pending {
		if _, err := tx.Exec(ctx,
			`UPDATE artifacts SET allowed_access = $2, allowed_write = $3, modified_at = now()
			  WHERE artifact_id = $1`,
			g.id, g.access, g.write); err != nil {
			return fmt.Errorf("pgstore: grant previous owner write: %w", err)
		}
	}
	return nil
}

// ownerMatches reports whether caller is owner, case-folded like every other
// email comparison in the ACL layer.
func ownerMatches(owner, caller string) bool {
	return caller != "" && owner != "" && strings.EqualFold(owner, caller)
}
