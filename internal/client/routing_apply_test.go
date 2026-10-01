package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRoutingApplyOwnerCeiling: the session OWNER's routing is a ceiling a guest body may
// only tighten (contract §1a, §9): min_tps is the max of the two, a guest quant list must
// sit inside the owner's rule or the request is refused locally, the owner's band (freq)
// can never be dropped or swapped from the body, confidential / self_hosted_only are OR'd,
// ignore is unioned, pref is a default the guest may override.
func TestRoutingApplyOwnerCeiling(t *testing.T) {
	owner := Routing{Pref: "cheap", MinTPS: 20, Confidential: true, SelfHostedOnly: true,
		Quantizations: []string{"Q8_0", "BF16", "unknown"}, Ignore: []string{"n9"}}
	type want struct {
		roger, provider map[string]any
		refusal         string
	}
	for name, tc := range map[string]struct {
		guest string
		want  want
	}{
		"no carrier: the owner's routing as is": {
			guest: `{"model":"m"}`,
			want: want{
				roger:    map[string]any{"pref": "cheap", "min_tps": 20.0, "confidential": true, "self_hosted_only": true},
				provider: map[string]any{"quantizations": []any{"Q8_0", "BF16", "unknown"}, "ignore": []any{"n9"}},
			},
		},
		"guest lowers min_tps: the owner's floor stands": {
			guest: `{"model":"m","roger":{"min_tps":5}}`,
			want:  want{roger: map[string]any{"min_tps": 20.0}},
		},
		"guest raises min_tps: the guest's floor is taken": {
			guest: `{"model":"m","roger":{"min_tps":40}}`,
			want:  want{roger: map[string]any{"min_tps": 40.0}},
		},
		"guest weakens confidential: still confidential": {
			guest: `{"model":"m","roger":{"confidential":false,"self_hosted_only":false}}`,
			want:  want{roger: map[string]any{"confidential": true, "self_hosted_only": true}},
		},
		"guest overrides pref: a default, so the guest wins": {
			guest: `{"model":"m","roger":{"pref":"fast"}}`,
			want:  want{roger: map[string]any{"pref": "fast"}},
		},
		"guest freq is dropped: the owner's band stands": {
			guest: `{"model":"m","roger":{"freq":"147.520 MHz ZZZZ-ZZZZ"}}`,
			want:  want{roger: map[string]any{"freq": nil}},
		},
		"guest narrows the quant rule": {
			guest: `{"model":"m","provider":{"quantizations":["bf16"]}}`,
			want:  want{provider: map[string]any{"quantizations": []any{"bf16"}}},
		},
		"guest quant outside the rule is refused": {
			guest: `{"model":"m","provider":{"quantizations":["Q4_K_M"]}}`,
			want:  want{refusal: "quantization Q4_K_M is outside this session's quant rule"},
		},
		"guest ignore is unioned with the owner's": {
			guest: `{"model":"m","provider":{"ignore":["n8"]}}`,
			want:  want{provider: map[string]any{"ignore": []any{"n8", "n9"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := owner.Apply([]byte(tc.guest))
			if tc.want.refusal != "" {
				var rr *RoutingRefusal
				if !errors.As(err, &rr) || rr.Msg != tc.want.refusal {
					t.Fatalf("err = %v, want refusal %q", err, tc.want.refusal)
				}
				if out != nil {
					t.Fatalf("a refused body must not be produced; got %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			var got struct {
				Roger    map[string]any `json:"roger"`
				Provider map[string]any `json:"provider"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			for k, v := range tc.want.roger {
				if !equalJSON(got.Roger[k], v) {
					t.Errorf("roger.%s = %v, want %v", k, got.Roger[k], v)
				}
			}
			for k, v := range tc.want.provider {
				if !equalJSON(got.Provider[k], v) {
					t.Errorf("provider.%s = %v, want %v", k, got.Provider[k], v)
				}
			}
			if strings.Contains(string(out), "ZZZZ-ZZZZ") {
				t.Errorf("the guest's band code reached the wire: %s", out)
			}
		})
	}
}

// TestFoldCallerCarriersOwnerCeiling: HeaderMode applies the same ceiling to a guest body
// before lifting it onto the header wire.
func TestFoldCallerCarriersOwnerCeiling(t *testing.T) {
	owner := Routing{MinTPS: 20, Quantizations: []string{"Q8_0", "unknown"}}
	r := owner
	out, dropped, hasModels, err := r.foldCallerCarriers([]byte(`{"model":"m","roger":{"min_tps":5,"freq":"ZZZZ"},"provider":{"quantizations":["q8_0"]}}`))
	if err != nil || hasModels {
		t.Fatalf("fold: err=%v hasModels=%v", err, hasModels)
	}
	if r.MinTPS != 20 {
		t.Errorf("min_tps lowered to %v; the owner's floor must stand", r.MinTPS)
	}
	if strings.Contains(string(out), "ZZZZ") || strings.Contains(string(out), "roger") {
		t.Errorf("carriers must leave the body: %s", out)
	}
	if strings.Join(dropped, ",") != "provider.quantizations" {
		t.Errorf("dropped = %v, want provider.quantizations (freq is the owner's, never reported)", dropped)
	}
	r = owner
	if _, _, _, err := r.foldCallerCarriers([]byte(`{"model":"m","provider":{"quantizations":["Q4_K_M"]}}`)); err == nil || !strings.Contains(err.Error(), "outside this session's quant rule") {
		t.Errorf("a guest quant outside the rule must be refused in header mode too; err = %v", err)
	}
	r = owner
	if _, _, _, err := r.foldCallerCarriers([]byte(`[1,2]`)); err == nil {
		t.Errorf("a non-object body must be an error")
	}
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
