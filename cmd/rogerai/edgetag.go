package main

// `roger edge tag` (features/edge/tags.feature): the owner's words for what a unit IS.
//   roger edge tag <node>                  show what it carries
//   roger edge tag <node> +production +gpu add
//   roger edge tag <node> -gpu             remove
//   roger edge tag <node> bench lab        replace the list
//   roger edge tag <node> --clear          carry none
// A tag names and groups. It is never a capability and never a permission: an alert rule may be
// scoped to one (`--scope tag:production`), and nothing else reads it.

import (
	"fmt"
	"strings"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func cmdEdgeTag(cfg config, args []string) error {
	leaf, _ := edgeLeaf("tag")
	// This leaf parses its own arguments: `-gpu` REMOVES a tag, and the shared parser would read it
	// as a flag. The rule is unambiguous and stated in the help - a token starting with "--" is a
	// flag, anything else is one of the owner's words.
	pos, clear := []string{}, false
	for _, a := range args {
		switch {
		case a == "--clear":
			clear = true
		case strings.HasPrefix(a, "--"):
			return usagef("roger edge tag: unknown flag %q", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return usagef("usage: roger edge tag %s", leaf.args)
	}
	if len(pos) > leaf.max {
		return usagef("roger edge tag takes at most %d tags", leaf.max-1)
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	nodes, err := st.list()
	if err != nil {
		return err
	}
	n, ok, err := edgeResolve(nodes, pos[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no node on this Edge is called %q", pos[0])
	}
	words := pos[1:]
	if len(words) == 0 && !clear {
		edgePrintTags(n)
		return nil
	}
	msg, err := edgeTagApply(st, n, words, clear)
	if err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// edgeTagApply is the one core the CLI, the screen and the console share: +word adds, -word removes,
// bare words replace the whole list, --clear empties it. Mixing a bare word with +/- is refused
// rather than guessed at, because "prod -bench" could mean either.
func edgeTagApply(st *edgeState, n store.EdgeNode, words []string, clear bool) (string, error) {
	if clear {
		if len(words) > 0 {
			return "", usagef("--clear takes no tags; it empties them")
		}
		if err := st.fleet.SetTags(n.ID, nil); err != nil {
			return "", err
		}
		return n.Name + " carries no tags now", nil
	}
	var add, remove, replace []string
	for _, w := range words {
		for _, part := range strings.Split(w, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			switch {
			case strings.HasPrefix(part, "+"):
				add = append(add, part[1:])
			case strings.HasPrefix(part, "-"):
				remove = append(remove, part[1:])
			default:
				replace = append(replace, part)
			}
		}
	}
	if len(replace) > 0 && (len(add) > 0 || len(remove) > 0) {
		return "", usagef("either name the tags this node carries, or change them with +tag and -tag - not both")
	}
	switch {
	case len(replace) > 0:
		if err := st.fleet.SetTags(n.ID, replace); err != nil {
			return "", err
		}
	default:
		if err := st.fleet.EditTags(n.ID, add, remove); err != nil {
			return "", err
		}
	}
	got, _, err := st.fleet.Get(n.ID)
	if err != nil {
		return "", err
	}
	if len(got.Tags) == 0 {
		return n.Name + " carries no tags now", nil
	}
	return n.Name + " carries " + strings.Join(got.Tags, " · "), nil
}

func edgePrintTags(n store.EdgeNode) {
	if len(n.Tags) == 0 {
		fmt.Printf("%s carries no tags. add one: roger edge tag %s +production\n", n.Name, n.Name)
		return
	}
	fmt.Printf("%s carries %s\n", n.Name, strings.Join(n.Tags, " · "))
	fmt.Printf("  a rule can cover the group: roger edge alert set cpu_temp gt 70 --scope %s\n", edge.TagScope(n.Tags[0]))
}

// edgeTagsLabel is the tags column and the readout row: the owner's words, or a dash.
func edgeTagsLabel(n store.EdgeNode) string {
	if len(n.Tags) == 0 {
		return "-"
	}
	return strings.Join(n.Tags, " ")
}
