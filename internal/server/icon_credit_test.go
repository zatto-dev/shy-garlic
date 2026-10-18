package server

import (
	"bytes"
	"html"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

// The icon collections' licenses ask for credit where the icons are used:
// CC BY 4.0 (selfh.st/icons) for title, author, source, license and whether
// the work was changed; Apache-2.0 (Dashboard Icons) for its license. The
// credit sits in the panel footer and under the icon picker, in both languages.
func TestIconCreditRendersInBothLanguages(t *testing.T) {
	s := testServer(t)
	links := []string{
		`href="https://icons.zatto.dev/attribution"`,
		`href="https://github.com/homarr-labs/dashboard-icons"`,
		`href="https://www.apache.org/licenses/LICENSE-2.0"`,
		`href="https://github.com/selfhst/icons"`,
		`href="https://creativecommons.org/licenses/by/4.0/"`,
	}
	texts := map[i18n.Lang]string{}
	for _, l := range []i18n.Lang{i18n.EN, i18n.PL} {
		d := fullPageView()
		d.T, d.TJSON, d.SecondLang, d.SecondLangName, d.SecondLangShort = fillLang(l)
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, "index.html", d); err != nil {
			t.Fatalf("index.html (%s): %v", l, err)
		}
		out := buf.String()
		if n := strings.Count(out, `class="icon-credit`); n != 2 {
			t.Errorf("%s: %d icon credits, want 2 (footer and icon picker)", l, n)
		}
		for _, link := range links {
			if strings.Count(out, link) != 2 {
				t.Errorf("%s: link %s should appear in both credits", l, link)
			}
		}
		m := i18n.Map(l)
		// look inside the credit paragraphs only — the page also embeds every
		// text as JSON for app.js, which would satisfy a page-wide search
		for _, c := range creditParagraphs(out) {
			for _, k := range []string{"icon_credit_label", "icon_credit_by", "icon_credit_changes"} {
				if !strings.Contains(c, html.EscapeString(m[k])) {
					t.Errorf("%s: the credit does not show text %q (%q)", l, k, m[k])
				}
			}
		}
		texts[l] = m["icon_credit_changes"]
	}
	if texts[i18n.EN] == texts[i18n.PL] {
		t.Error("the credit text is not translated")
	}
}
