package webui

// Review 2026-10-27 (a semantic conflict from the main merge): main added a CATEGORY column to the
// SHARE table, and roger's own rows (served, on disk, the fetch, get a model) kept the old eight
// cells, so their buttons drew under CATEGORY. Every row spans the header's columns.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRogersOwnShareRowsSpanTheHeadersColumns(t *testing.T) {
	share := panelOf(t, consoleHTML(t), "panel-share")
	head := share[strings.Index(share, "<thead>"):strings.Index(share, "</thead>")]
	cols := len(regexp.MustCompile(`<th[ >]`).FindAllString(head, -1))
	require.Equal(t, 9, cols, "the SHARE header has nine columns, CATEGORY among them")

	js := consoleJS(t)
	a := strings.Index(js, `var tr0 = el("tr", "is-serving");`)
	b := strings.Index(js[a:], `gb.onclick`)
	require.True(t, a > 0 && b > 0, "roger's own rows are built in console.js")
	rows := js[a : a+b]
	for _, m := range regexp.MustCompile(`colSpan = (\d+)`).FindAllStringSubmatch(rows, -1) {
		n, _ := strconv.Atoi(m[1])
		require.Equal(t, cols, n, "a full-width row of roger's spans every column")
	}
	require.Equal(t, 2, strings.Count(rows, `"cat-cell"`), "a served row and an on-disk row each carry the CATEGORY cell")
}
