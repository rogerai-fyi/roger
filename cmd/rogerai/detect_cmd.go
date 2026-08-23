package main

import (
	"fmt"
	"sort"
	"strings"

	"rogerai.fm/roger/v6/internal/detect"
)

// detectScan is the seam. The real scan talks to whatever is listening on this machine,
// which a test must never do - it would pass or fail on the developer's running ollama.
var detectScan = func() ([]detect.Found, []string) { return detect.DetectFull() }

// cmdDetect prints what this machine's runtimes and model files actually say, WITHOUT
// going on air. It exists because every other view of detection required publishing:
// the band card shows one model, the dial shows the market. An operator asking "what
// will the market see me as?" should not have to broadcast to find out.
//
// Everything printed is measured. A model whose runtime and file said nothing about its
// compression prints an em dash - never a label guessed from the model id, and never a
// blank that reads as "not scanned".
func cmdDetect(args []string) error {
	verbose := false
	for _, a := range args {
		switch a {
		case "-v", "--verbose":
			verbose = true
		case "-h", "--help":
			fmt.Println("usage: roger detect [-v]\n\nprints the local servers and what was detected about each model.")
			return nil
		}
	}

	found, needKey := detectScan()
	if len(found) == 0 {
		fmt.Println("no local OpenAI-compatible server answered.")
		fmt.Println("start ollama, llama.cpp, LM Studio, or vLLM and run this again.")
		for _, u := range needKey {
			fmt.Printf("  %s is serving but needs a key (set it and rerun)\n", u)
		}
		return nil
	}

	for _, f := range found {
		fmt.Printf("%s  %s\n", f.Name, f.BaseURL)
		models := append([]string(nil), f.Models...)
		sort.Strings(models)
		if len(models) == 0 {
			fmt.Println("  (serving, but reported no models)")
			continue
		}
		w := 0
		for _, m := range models {
			if len(m) > w {
				w = len(m)
			}
		}
		if w > 44 {
			w = 44
		}
		for _, m := range models {
			fmt.Printf("  %-*s  %s\n", w, trunc(m, w), detectLine(f, m, verbose))
		}
		fmt.Println()
	}
	for _, u := range needKey {
		fmt.Printf("%s is serving but needs a key\n", u)
	}
	return nil
}

// detectLine is the per-model right-hand column: the variant fields, stated as absent
// when absent. The dash is deliberate - an empty column cannot tell "this model
// published no metadata" apart from "this row was never scanned".
func detectLine(f detect.Found, m string, verbose bool) string {
	parts := []string{}
	if q := f.Quant[m]; q != "" {
		parts = append(parts, q)
	}
	if w := f.Weights[m]; w != "" {
		parts = append(parts, "by "+w)
	}
	if v := f.Variant[m]; v != "" {
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		parts = append(parts, "—")
	}
	line := strings.Join(parts, " · ")
	if verbose {
		if k := f.Modality[m]; k != "" && k != "chat" {
			line += "   [" + k + "]"
		}
		if c := f.Capabilities[m]; len(c) > 0 {
			line += "   [" + strings.Join(c, ",") + "]"
		}
		if n := f.Ctx[m]; n > 0 {
			line += fmt.Sprintf("   ctx %d", n)
		}
	}
	return line
}

func trunc(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
