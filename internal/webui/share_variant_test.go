package webui

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// asset reads one of the embedded console files off disk. These are the shipped assets,
// so a test that reads them is testing what the browser actually receives.
func asset(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("assets/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// countTH counts header cells. It must NOT match "<thead>", which a bare "<th" prefix
// does - that off-by-one made this test's first run report a phantom extra column.
func countTH(s string) int { return len(regexp.MustCompile(`<th[ >]`).FindAllString(s, -1)) }

// The SHARE table is index-addressed: buildShareRow appends cells in order and
// updateShareRow writes them back by number. Inserting a column shifts every index after
// it, and getting that wrong does not crash - it silently writes the price into the
// status cell and the earnings into the counters, which reads as plausible data.
//
// This asserts the two halves agree: as many <th> in the markup as cells appended, and a
// highest index that matches.
func TestShareTableHeaderAndCellsAgree(t *testing.T) {
	html, js := asset(t, "console.html"), asset(t, "console.js")

	// the SHARE table's header row
	i := strings.Index(html, `class="data-table share-table"`)
	if i < 0 {
		t.Fatal("share table not found")
	}
	head := html[i:]
	if j := strings.Index(head, "</thead>"); j > 0 {
		head = head[:j]
	}
	headers := countTH(head)

	// the cells buildShareRow appends
	b := strings.Index(js, "function buildShareRow(")
	if b < 0 {
		t.Fatal("buildShareRow not found")
	}
	body := js[b:]
	if j := strings.Index(body, "\n  }"); j > 0 {
		body = body[:j]
	}
	cells := strings.Count(body, "tr.appendChild(")

	if headers != cells {
		t.Fatalf("%d header columns but %d cells appended - the table is misaligned", headers, cells)
	}

	// updateShareRow must not reach past the last cell
	u := strings.Index(js, "function updateShareRow(")
	if u < 0 {
		t.Fatal("updateShareRow not found")
	}
	upd := js[u:]
	if j := strings.Index(upd, "\n  }\n"); j > 0 {
		upd = upd[:j]
	}
	max := -1
	for _, m := range regexp.MustCompile(`tds\[(\d+)\]`).FindAllStringSubmatch(upd, -1) {
		n, _ := strconv.Atoi(m[1])
		if n > max {
			max = n
		}
	}
	if max != cells-1 {
		t.Fatalf("highest cell index is %d but %d cells exist - a column shift was applied unevenly", max, cells-1)
	}
}

// The empty-row colspan has to span the whole table or the placeholder sits under part of
// it. It is the one number that does not fail loudly when a column is added.
func TestShareEmptyRowSpansEveryColumn(t *testing.T) {
	html, js := asset(t, "console.html"), asset(t, "console.js")

	i := strings.Index(html, `class="data-table share-table"`)
	head := html[i:]
	if j := strings.Index(head, "</thead>"); j > 0 {
		head = head[:j]
	}
	want := strconv.Itoa(countTH(head))

	// the markup placeholder
	tb := html[i:]
	if j := strings.Index(tb, "</tbody>"); j > 0 {
		tb = tb[:j]
	}
	if !strings.Contains(tb, `colspan="`+want+`"`) {
		t.Fatalf("share placeholder does not span %s columns: %s", want, tb[strings.Index(tb, "empty-row"):])
	}

	// and the one the renderer builds
	r := strings.Index(js, "function renderShareRows(")
	body := js[r:]
	if j := strings.Index(body, "\n  }\n"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "td.colSpan = "+want) {
		t.Fatalf("renderShareRows builds a placeholder that does not span %s columns", want)
	}
}

// The VARIANT column must exist and must render its own absence. A blank cell cannot tell
// "this model published no metadata" apart from "this column failed", and the operator
// should only shrug at one of those.
func TestShareVariantColumnStatesItsAbsence(t *testing.T) {
	html, js := asset(t, "console.html"), asset(t, "console.js")
	if !strings.Contains(html, "VARIANT") {
		t.Fatal("no VARIANT column in the share table")
	}
	if !strings.Contains(js, `"variant-none", "—"`) {
		t.Fatal("an undetected variant renders as a blank rather than as an em dash")
	}
	for _, field := range []string{"row.quant", "row.weights", "row.variant"} {
		if !strings.Contains(js, field) {
			t.Fatalf("%s is never read - the wire field is dropped on the floor", field)
		}
	}
}
