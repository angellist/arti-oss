package pgstore

import (
	"context"
	"strings"
)

// people.go — the "who exists in arti" lookup behind the email typeaheads in
// the access editor and the group-membership editor.
//
// arti has no users table: it never provisions an account, it just trusts the
// identity its auth layer hands it. So the set of people it knows is implicit,
// spread across the tables that happen to have recorded an email:
//
//   - user_idp_groups — one row per interactive login, so this is effectively
//     "everyone who has ever signed in", and the broadest of the four.
//   - artifacts.creator — includes people who only ever wrote through an API
//     key or an agent, and so may never have appeared in a browser session.
//   - user_groups.members — someone already granted by a colleague. Glob members
//     (`*@domain`, `*`) are patterns rather than people and are excluded.
//   - role_assignments — a user principal carrying a role.
//   - api_keys.owner_email — a principal that authenticates by key and may never
//     open a browser at all, which is what most service accounts look like.
//   - comments.author, comment_threads.created_by / .resolved_by — someone whose
//     only trace is having said something on a document, or having tidied up
//     after somebody who did.
//   - users.email — a principal recorded deliberately (see migration 0023),
//     which is the only arm that can name one who has done nothing else yet.
//
// The union is what a person typing an address means by "my colleagues". None
// of these sources is authoritative on its own, and none can be made
// authoritative without arti taking on user provisioning it deliberately does
// not do.
//
// The CTE below is shared with the Users roster (users.go). Both read the same
// definition of "who arti knows" on purpose: two unions would drift, and a
// principal visible on one surface but not the other is the bug that costs the
// most trust. What is NOT shared is disclosure — this endpoint answers a
// bounded typeahead for any authenticated caller, while the roster enumerates
// and is gated on MANAGE_ROLES. Widening the CTE must never widen that.

// knownPrincipalsCTE yields one lowercased `email` column per principal arti
// knows. Callers wrap it in `WITH known AS (…)`. Glob members and role
// principals are patterns rather than people; both are filtered by the caller
// (`email NOT LIKE '%*%'` plus an `@` check), not here, so a caller cannot
// forget the arm-specific half of the rule.
const knownPrincipalsCTE = `
		    SELECT lower(email) AS email FROM user_idp_groups
		    UNION
		    SELECT DISTINCT lower(creator) FROM artifacts WHERE creator <> ''
		    UNION
		    SELECT DISTINCT lower(m) FROM user_groups, unnest(members) AS m
		    UNION
		    SELECT lower(principal_id) FROM role_assignments WHERE principal_type = 'user'
		    UNION
		    SELECT DISTINCT lower(owner_email) FROM api_keys WHERE owner_email <> ''
		    UNION
		    SELECT DISTINCT lower(author) FROM comments WHERE author <> ''
		    UNION
		    SELECT DISTINCT lower(created_by) FROM comment_threads WHERE created_by <> ''
		    UNION
		    SELECT DISTINCT lower(resolved_by) FROM comment_threads
		     WHERE resolved_by IS NOT NULL AND resolved_by <> ''
		    UNION
		    SELECT lower(email) FROM users`

// KnownPrincipalSources names, in the CTE's own order, every column the union
// above reads. It lives here so adding an arm without naming it is a one-line
// diff away from obvious: GET /api/users reports this list, and a roster that
// under-reports what it read is worse than one that reads less.
var KnownPrincipalSources = []string{
	"user_idp_groups.email",
	"artifacts.creator",
	"user_groups.members",
	"role_assignments.principal_id",
	"api_keys.owner_email",
	"comments.author",
	"comment_threads.created_by",
	"comment_threads.resolved_by",
	"users.email",
}

// KnownPerson is one directory hit. Email only: arti stores no display names,
// and inventing one from the local part would be a guess presented as a fact.
type KnownPerson struct {
	Email string
}

// MinPeopleQuery is the shortest query SearchKnownPeople answers. Below it the
// result is empty, which is what keeps this a typeahead rather than a roster
// dump: there is no request that returns "everyone".
const MinPeopleQuery = 2

// SearchKnownPeople returns emails matching q, best match first: exact, then
// prefix, then anything else, alphabetical within each tier. A query shorter
// than MinPeopleQuery returns nothing.
//
// Ranking is done in SQL rather than in Go because the union is already a
// database-side merge — sorting it here would mean pulling every match across
// four tables into memory to reorder a list of eight.
func (s *Store) SearchKnownPeople(ctx context.Context, q string, limit int) ([]KnownPerson, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if len([]rune(q)) < MinPeopleQuery {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	// Escape the LIKE metacharacters before wrapping in %…%, or a typed `%`
	// matches everything and turns the two-character floor into no floor at all.
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	like := "%" + esc + "%"

	rows, err := s.pool.Query(ctx, `
		WITH known AS (`+knownPrincipalsCTE+`
		)
		SELECT email FROM known
		WHERE email LIKE $1 ESCAPE '\'
		  -- Glob members and role principals are patterns, not people; offering
		  -- one as a suggestion would silently widen a grant to a whole domain.
		  AND email NOT LIKE '%*%'
		  AND position('@' in email) > 1
		ORDER BY (email = $2) DESC, (email LIKE $3 ESCAPE '\') DESC, email
		LIMIT $4`,
		like, q, esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]KnownPerson, 0, limit)
	for rows.Next() {
		var p KnownPerson
		if err := rows.Scan(&p.Email); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
