package artifacts

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var eidAttrRE = regexp.MustCompile(` data-arti-eid="\d+"`)

const editFixture = `<!doctype html><html><head><title>Deck</title><style>li{color:red}</style>
<script>var s = "<li>not a tag</li>";</script></head>
<body><section class="slide"><h1>Quarterly review</h1>
<ul><li>Revenue <b>up</b> 12%</li><li class="x">Costs flat</li><li>Hiring &amp; growth</li></ul>
<p>Unclosed para<p>Second para</p><img src="a.png"><br/>
<div id="gen"></div></section></body></html>`

func eidOf(t *testing.T, tagged, needle string) int {
	t.Helper()
	i := strings.Index(tagged, needle)
	if i < 0 {
		t.Fatalf("%q not in tagged page", needle)
	}
	m := regexp.MustCompile(`data-arti-eid="(\d+)"`).FindStringSubmatch(tagged[i:])
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestTagHTMLForEditRoundTrips(t *testing.T) {
	tagged := string(tagHTMLForEdit([]byte(editFixture)))
	if got := eidAttrRE.ReplaceAllString(tagged, ""); got != editFixture {
		t.Fatalf("stripping eids does not restore the source:\n%s", got)
	}
	if strings.Contains(tagged, `<li data-arti-eid="`) && strings.Contains(tagged, `"<li data-arti-eid`) {
		t.Fatal("tagged a tag inside a script string")
	}
	if !strings.Contains(tagged, `<br data-arti-eid="`) || !strings.Contains(tagged, `<li data-arti-eid="`) {
		t.Fatalf("expected tagged li and br: %s", tagged)
	}
}

func TestApplyHTMLTextEdits(t *testing.T) {
	tagged := string(tagHTMLForEdit([]byte(editFixture)))
	li1 := eidOf(t, tagged, "<li data-arti-eid")
	li3 := eidOf(t, tagged, `<li data-arti-eid="`+strconv.Itoa(li1+3)) // third li: li1, b, li2, li3
	h1 := eidOf(t, tagged, "<h1 ")

	out, rejected := applyHTMLTextEdits([]byte(editFixture), []htmlTextEdit{
		{EID: h1, Before: "Quarterly review", HTML: "Q3 review"},
		{EID: li1, Before: "Revenue up 12%", HTML: `Revenue <b data-arti-eid="9">up</b> 14%`},
		{EID: li3, Before: "Hiring & growth", HTML: "Hiring <script>alert(1)</script><img src=x onerror=alert(1)>& <a href=\"javascript:alert(1)\" onclick=\"x\">growth</a>"},
	})
	if len(rejected) != 0 {
		t.Fatalf("unexpected rejections %v", rejected)
	}
	got := string(out)
	for _, want := range []string{
		"<h1>Q3 review</h1>",
		"<li>Revenue <b>up</b> 14%</li>",
		`<li class="x">Costs flat</li>`,
		"<li>Hiring &amp; <a>growth</a></li>",
		`var s = "<li>not a tag</li>";`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "data-arti-eid") || strings.Contains(got, "alert") {
		t.Errorf("edit leaked tagging or script:\n%s", got)
	}
}

func TestApplyHTMLTextEditsRejects(t *testing.T) {
	tagged := string(tagHTMLForEdit([]byte(editFixture)))
	li2 := eidOf(t, tagged, `<li data-arti-eid`) + 2
	unclosedP := eidOf(t, tagged, "<p ")
	gen := eidOf(t, tagged, `<div data-arti-eid`)
	img := eidOf(t, tagged, "<img ")
	cases := []htmlTextEdit{
		{EID: li2, Before: "Costs rising", HTML: "x"},        // text changed by script
		{EID: unclosedP, Before: "Unclosed para", HTML: "x"}, // no explicit close before the next <p>
		{EID: gen, Before: "made by JS", HTML: "x"},          // empty in source
		{EID: img, Before: "", HTML: "x"},                    // void element
		{EID: 99999, Before: "", HTML: "x"},                  // no such tag
	}
	out, rejected := applyHTMLTextEdits([]byte(editFixture), cases)
	if len(rejected) != len(cases) {
		t.Fatalf("rejected %v, want all %d", rejected, len(cases))
	}
	if string(out) != editFixture {
		t.Fatal("source changed although every edit was rejected")
	}
}

func TestApplyHTMLTextEditsRejectsNonInlineChildren(t *testing.T) {
	src := `<body><p>Caption<div id="chart"></div></p><p>Icon <svg/> here</p></body>`
	tagged := string(tagHTMLForEdit([]byte(src)))
	p1 := eidOf(t, tagged, "<p ")
	p2 := eidOf(t, tagged, `<p data-arti-eid="`+strconv.Itoa(p1+2))
	out, rejected := applyHTMLTextEdits([]byte(src), []htmlTextEdit{
		{EID: p1, Before: "Caption", HTML: "New caption"},
		{EID: p2, Before: "Icon here", HTML: "x"},
	})
	if len(rejected) != 2 || string(out) != src {
		t.Fatalf("rejected=%v out=%s", rejected, out)
	}
}

func TestSanitizeEditHTMLKeepsAttributesAndQuotes(t *testing.T) {
	in := `It's "q" <span aria-label="n" role="note">a</span> <font face="Arial" size="2">b</font>&nbsp;c`
	want := `It's "q" <span aria-label="n" role="note">a</span> <font face="Arial" size="2">b</font>&nbsp;c`
	if got := sanitizeEditHTML(in); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSafeEditHref(t *testing.T) {
	for _, h := range []string{"https://x.com/a", "/s/doc", "#slide-3", "mailto:a@b.com", "doc.html?x=1"} {
		if !safeEditHref(h) {
			t.Errorf("rejected %q", h)
		}
	}
	for _, h := range []string{"javascript:alert(1)", "\x01javascript:alert(1)", " java\tscript:x", "JaVaScRiPt:x", "data:text/html,x", "vbscript:x"} {
		if safeEditHref(h) {
			t.Errorf("kept %q", h)
		}
	}
}
