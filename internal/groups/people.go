package groups

import (
	"net/http"
	"strconv"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// people.go — GET /api/people?q=&limit=, the email typeahead behind the access
// editor and the group-membership editor.
//
// It lives beside the group routes because it answers the other half of the
// same question: those fields accept a person OR a group, and until now only
// the group half could be searched. An email had to be typed from memory and a
// misspelling was indistinguishable from a colleague who had not signed in yet
// — which stays true after a typo is made, because arti grants to an address,
// not to an account.
//
// Disclosure is bounded the same way the IdP-group route is: this returns
// addresses that already appear in artifact metadata, group rosters, and role
// assignments visible across the workspace, and it returns them only to an
// authenticated caller, only two characters at a time, ten at a time. What it
// deliberately does NOT do is answer an empty query — there is no request shape
// here that enumerates the workspace.
type peopleDTO struct {
	Email string `json:"email"`
}

const defaultPeopleLimit = 10

func (s *Service) listPeople(w http.ResponseWriter, r *http.Request) {
	limit := defaultPeopleLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	// A short query is answered with an empty list and a 200, not a 400: the
	// client is not malformed, it is one keystroke early, and the field fires on
	// every keystroke by design. SearchKnownPeople enforces the floor itself, so
	// the rule cannot be bypassed by calling the store from somewhere else.
	people, err := s.store.SearchKnownPeople(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]peopleDTO, 0, len(people))
	for _, p := range people {
		out = append(out, peopleDTO{Email: p.Email})
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": out, "min_query": pgstore.MinPeopleQuery})
}
