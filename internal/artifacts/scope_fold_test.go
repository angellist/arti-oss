package artifacts

import (
	"reflect"
	"testing"
)

// parseLegacyScope must heal a deprecated single `scope` input the same way
// the 0005 migration backfill does, so an old client sending a mis-encoded
// scope can't re-introduce the phantom bracket scope-type.
func TestParseLegacyScope(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"   ", []string{}},
		{"a:bt-auto-route", []string{"a:bt-auto-route"}},
		{"topic:platform:auth", []string{"topic:platform:auth"}}, // colons preserved
		{`"quoted-scope"`, []string{"quoted-scope"}},             // JSON string unwrapped
		{`["solo"]`, []string{"solo"}},                           // 1-elem array
		{`["a:bt-auto-route","u:lavina.kalwani"]`, []string{"a:bt-auto-route", "u:lavina.kalwani"}},
		{`[" a "," ","b"]`, []string{"a", "b"}}, // trimmed, empties dropped
		{"[bad", []string{"[bad"}},              // malformed JSON kept verbatim
	}
	for _, c := range cases {
		got := parseLegacyScope(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseLegacyScope(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
