package tower

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The shipped example configurations are the only thing standing between an operator and
// reading Go structs to find out what a Tower config looks like. An example that has
// silently rotted is WORSE than none: it is wrong with an air of authority, and the
// operator who pastes it gets a strict-decode failure naming a field they never chose.
//
// So they are tested like code. Two different rots are possible and they need two
// different checks - the first would not catch the second:
//
//  1. The live YAML stops parsing, because a field was renamed or a rule tightened.
//     TestExampleConfigsParse catches that: it runs the REAL loader, the one the CLI
//     calls, not a relaxed copy.
//  2. A COMMENTED-OUT option outlives the field it documents. Nothing parses those lines,
//     so nothing notices - and they are the lines most likely to be copied, since they
//     are exactly the settings an operator goes looking for. TestExampleConfigKeysExist
//     catches it by checking every key in the file, commented or not, against the yaml
//     tags the structs actually declare.
const exampleDir = "../../packaging/tower"

func exampleFiles(t *testing.T) map[string]Mode {
	t.Helper()
	return map[string]Mode{
		"tower.standalone.example.yaml": ModeStandalone,
		"tower.joined.example.yaml":     ModeJoined,
	}
}

func TestExampleConfigsParse(t *testing.T) {
	for name, want := range exampleFiles(t) {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(exampleDir, name))
			if err != nil {
				t.Fatalf("read example: %v", err)
			}
			c, err := ParseConfig(b)
			if err != nil {
				t.Fatalf("the shipped example does not parse: %v", err)
			}
			if c.Mode != want {
				t.Fatalf("mode = %q, want %q", c.Mode, want)
			}
		})
	}
}

// yamlTags walks a struct type and collects every yaml key it can accept, following
// pointers, slices and maps so a nested block's fields are included.
func yamlTags(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag != "" && tag != "-" {
			out[tag] = true
		}
		yamlTags(f.Type, seen, out)
	}
}

// keyLine matches a YAML key at the start of a line, whether or not it is commented out.
var keyLine = regexp.MustCompile(`^\s*#?\s*([A-Za-z][A-Za-z0-9]*):`)

func TestExampleConfigKeysExist(t *testing.T) {
	known := map[string]bool{}
	yamlTags(reflect.TypeOf(Config{}), map[reflect.Type]bool{}, known)
	if len(known) == 0 {
		t.Fatal("collected no yaml tags from Config: the reflection walk is broken, and an " +
			"empty allowlist would make this test pass for everything")
	}

	for name := range exampleFiles(t) {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(exampleDir, name))
			if err != nil {
				t.Fatalf("read example: %v", err)
			}
			checked := 0
			for i, line := range strings.Split(string(b), "\n") {
				m := keyLine.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				key := m[1]
				checked++
				if !known[key] {
					t.Errorf("line %d: %q is not a field of tower.Config.\n"+
						"An example - including a commented-out one - must only name settings that "+
						"exist. If the field was removed, delete the line; if renamed, rename it.\n"+
						"  %s", i+1, key, strings.TrimSpace(line))
				}
			}
			if checked == 0 {
				t.Fatal("no keys found in the example: the regex matched nothing, so this test " +
					"would pass on an empty file")
			}
			t.Logf("checked %d keys (live and commented)", checked)
		})
	}
}
