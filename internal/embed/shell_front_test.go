package embed

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The Front shell substitutes the live conversation id into shell_query and
// appends the result to the doc URL verbatim. The query TEMPLATE is
// operator-configured, but the id is not — an id carrying & or = would split
// into extra params and corrupt slug / auth_secret parsing downstream. So the
// id must be percent-encoded at substitution time, while the template's own
// `=` stays structural.
func TestFrontShell_EncodesConversationIDInQuery(t *testing.T) {
	rec := httptest.NewRecorder()
	serveFrontShell(rec, "front", Surface{
		Origin:     []string{"https://app.frontapp.com"},
		ShellSlug:  "deployment-panel-app",
		ShellQuery: "ctx=deploy-ctx-{id}",
	})
	page := rec.Body.String()

	if !strings.Contains(page, `SHELL_QUERY.replace("{id}", encodeURIComponent(cnv))`) {
		t.Error("shell substitutes {id} into shell_query without percent-encoding it")
	}
	if strings.Contains(page, `SHELL_QUERY.replace("{id}", cnv)`) {
		t.Error("shell still contains the unencoded {id} substitution")
	}
	// The template itself must survive intact — encoding the whole fragment
	// would turn its structural `=` into %3D and break the param.
	if !strings.Contains(page, `'ctx=deploy-ctx-{id}'`) {
		t.Errorf("shell_query template not injected verbatim; page has %q", page)
	}
}
