package artifacts

import (
	"math"
	"net/url"
	"strconv"
	"strings"
)

// appParamSpec is one entry in an APP manifest's optional "params" array. It
// declares a URL query parameter the app accepts, so an iframe embedder knows
// the contract and the server can coerce/default the value before handing the
// app a typed object (window.arti.params). See resolveAppParams.
type appParamSpec struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`     // string|number|boolean|enum; "" ⇒ string
	Default  any      `json:"default"`  // value when the query omits this param
	Values   []string `json:"values"`   // allowed values when Type=="enum"
	Required bool     `json:"required"` // missing-required is a non-fatal note, never a page error
}

const (
	// maxAppParams caps how many declared params we resolve — defensive against a
	// pathological manifest. maxAppParamLen caps a single resolved value's byte
	// length — defensive against a giant query value reflected into the page.
	maxAppParams   = 32
	maxAppParamLen = 8192
)

// paramError records a non-fatal coercion problem, surfaced to the app as an
// entry in window.arti.params._errors so it can decide how to react.
type paramError struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// resolveAppParams coerces the request query against the declared param specs,
// leniently: a missing value falls back to the declared default (or the type's
// zero value); a malformed value falls back too and is recorded in _errors;
// unknown query keys are ignored (the page can still read them via
// location.search). It never fails — an embedded iframe must always render.
// Returns nil when nothing is declared, so the bridge surfaces an empty {}.
//
// Names beginning with "_" are reserved (the _errors channel) and skipped.
func resolveAppParams(specs []appParamSpec, q url.Values) map[string]any {
	if len(specs) == 0 {
		return nil
	}
	if len(specs) > maxAppParams {
		specs = specs[:maxAppParams]
	}
	out := make(map[string]any, len(specs)+1)
	errs := []paramError{}
	for _, sp := range specs {
		if sp.Name == "" || strings.HasPrefix(sp.Name, "_") {
			continue
		}
		present := q.Has(sp.Name)
		raw := q.Get(sp.Name)
		if len(raw) > maxAppParamLen {
			raw = raw[:maxAppParamLen]
			errs = append(errs, paramError{sp.Name, "value truncated to " + strconv.Itoa(maxAppParamLen) + " bytes"})
		}
		val, reason := coerceParam(sp, present, raw)
		if reason != "" {
			errs = append(errs, paramError{sp.Name, reason})
		}
		out[sp.Name] = val
	}
	out["_errors"] = errs
	return out
}

// coerceParam converts one raw query value to the spec's declared type. On a
// missing or malformed value it returns the spec's default and a non-empty
// reason string describing the fallback (empty reason ⇒ clean).
func coerceParam(sp appParamSpec, present bool, raw string) (any, string) {
	typ := sp.Type
	if typ == "" {
		typ = "string"
	}
	if !present {
		if sp.Required {
			return defaultFor(sp, typ), "required param missing"
		}
		return defaultFor(sp, typ), ""
	}
	switch typ {
	case "string":
		return raw, ""
	case "number":
		n, err := strconv.ParseFloat(raw, 64)
		// Reject non-finite values: ParseFloat accepts "NaN"/"Inf" without error,
		// but json.Marshal can't encode them — that would blank the whole injected
		// __ARTI_APP__ config and break the bridge. Fall back to the default.
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return defaultFor(sp, typ), "not a finite number: " + clip(raw)
		}
		return n, ""
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return defaultFor(sp, typ), "not a boolean: " + clip(raw)
		}
		return b, ""
	case "enum":
		for _, v := range sp.Values {
			if v == raw {
				return raw, ""
			}
		}
		return defaultFor(sp, typ), "not an allowed value: " + clip(raw)
	default:
		return raw, "unknown type " + typ + ", treated as string"
	}
}

// clip shortens an echoed query value for inclusion in an _errors reason. The
// raw value can be up to maxAppParamLen (8KB); echoing it verbatim across many
// malformed params would let a crafted URL amplify the injected JSON. 60 chars
// keeps the diagnostic useful without the amplification.
func clip(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// defaultFor returns the declared default, or the type's zero value when none
// is declared. Authors are expected to declare a default matching the type;
// a mismatched default is passed through verbatim (lenient).
func defaultFor(sp appParamSpec, typ string) any {
	if sp.Default != nil {
		return sp.Default
	}
	switch typ {
	case "number":
		return float64(0)
	case "boolean":
		return false
	default: // string, enum, unknown
		return ""
	}
}
