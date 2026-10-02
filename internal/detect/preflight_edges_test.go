package detect

// THE EDGES OF THE HARDWARE REPORT: each way a figure can be unreadable, each unit a vendor tool
// prints, and the words an operator reads for each. These are the branches a GPU-less CI box never
// reaches through the gatherers, so they are driven here from struct literals and real tool output.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckStatusPrintsAWordForEveryValue(t *testing.T) {
	require.Equal(t, "OK", CheckMet.String())
	require.Equal(t, "BELOW", CheckBelow.String())
	require.Equal(t, "UNKNOWN", CheckUnknown.String())
	require.Equal(t, "UNKNOWN", CheckStatus(42).String(), "an out-of-range status never prints as a pass")
}

func TestAcceleratorCheckSaysWhichFigureItCouldNotRead(t *testing.T) {
	// Apple Silicon with no RAM figure: unified memory is unknown, not "no GPU".
	hw := goodBox()
	hw.UnifiedMemory, hw.RAMKnown = true, false
	c := acceleratorCheck(hw)
	require.Equal(t, CheckUnknown, c.Status)
	require.Equal(t, "GPU memory (unified)", c.Name)
	require.Contains(t, c.Detail, "unified memory")

	// A GPU class with nothing enumerated: no tooling answered.
	hw = goodBox()
	hw.GPUs = nil
	c = acceleratorCheck(hw)
	require.Equal(t, CheckUnknown, c.Status)
	require.Contains(t, c.Detail, "no GPU tooling answered")

	// GPUs present, memory silent.
	hw = goodBox()
	hw.GPUs = []LocalGPU{{Model: "A"}, {Model: "B"}}
	hw.VRAMKnown = false
	c = acceleratorCheck(hw)
	require.Equal(t, CheckUnknown, c.Status)
	require.Equal(t, "2 GPU(s) present, but their memory size could not be read", c.Detail)

	// Each unknown makes the whole verdict INCOMPLETE, never a pass.
	p := Assess(hw)
	require.Equal(t, VerdictIncomplete, p.Verdict)
	require.False(t, p.Clears())
}

func TestCPUCoresUnreadableIsUnknownAndTwoCoresIsBelow(t *testing.T) {
	hw := goodBox()
	hw.CPUCores = 0
	c := cpuCheck(hw)
	require.Equal(t, CheckUnknown, c.Status)
	require.Equal(t, "could not be read on this host", c.Detail)

	hw.CPUCores = 2
	c = cpuCheck(hw)
	require.Equal(t, CheckBelow, c.Status)
	require.Equal(t, "2", c.Detail)
	require.Equal(t, "4", c.Bar)
}

func TestSmallFiguresPrintInMiBNotAsZeroGiB(t *testing.T) {
	require.Equal(t, "512 MiB", mib(512))
	require.Equal(t, "1023 MiB", mib(1023))
	require.Equal(t, "1.0 GiB", mib(1024))
	require.Equal(t, "8.0 GiB", mib(BarVRAMMiB))
}

func TestAnIncompleteReportSaysItDoesNotKnow(t *testing.T) {
	cs := Consequences(VerdictIncomplete)
	require.Len(t, cs, 2)
	require.Contains(t, cs[0], "does not know")
	require.Contains(t, cs[1], "nothing here gates `roger share`")

	hw := goodBox()
	hw.GPUs = []LocalGPU{{Model: "Silent Card"}}
	hw.VRAMKnown = false
	s := Assess(hw).String()
	require.Contains(t, s, "GPU: Silent Card (memory not reported)")
	require.Contains(t, s, "preflight: INCOMPLETE")
	require.Contains(t, s, "it is saying it does not know")
}

func TestNoteIsExportedWithTheSameDeduplication(t *testing.T) {
	var hw LocalHW
	hw.Note("RAM: /proc/meminfo is not mounted")
	hw.Note("RAM: /proc/meminfo is not mounted")
	hw.Note("disk: statfs failed")
	require.Equal(t, []string{"RAM: /proc/meminfo is not mounted", "disk: statfs failed"}, hw.Undetermined)
}

func TestSetGPUsWithNoDevicesIsTheCPUBucketWithNoVRAM(t *testing.T) {
	var hw LocalHW
	hw.SetGPUs(nil)
	require.Equal(t, BucketGPUCount(0), hw.Class)
	require.False(t, hw.VRAMKnown)
	require.Empty(t, hw.Undetermined, "no GPU is a measurement, not an undetermined figure")

	hw.SetGPUs([]LocalGPU{{Model: "a", VRAMMiB: 24564}, {Model: "b", VRAMMiB: 24564}})
	require.True(t, hw.VRAMKnown)
	require.Equal(t, 49128, hw.VRAMTotalMiB)
}

func TestSetVRAMTotalIgnoresANonPositiveFigure(t *testing.T) {
	var hw LocalHW
	hw.SetGPUs([]LocalGPU{{Model: "AMD GPU"}})
	for _, n := range []int{0, -5} {
		hw.SetVRAMTotal(n)
		require.False(t, hw.VRAMKnown, "a total of %d is not a measurement", n)
		require.Equal(t, []string{vramUnreportedNote}, hw.Undetermined, "the note stays until a real total arrives")
	}
}

func TestNvidiaMemoryColumnsInEveryUnitTheToolPrints(t *testing.T) {
	gpus := ParseNvidiaGPUs("NVIDIA A100, 81920 MiB\nNVIDIA H100, 80 GiB\nOld Card, 24 GB\nOdd, 512 KiB\nWeird, 4096 kB\nMissing, N/A\nBare, 24564\nNoComma\n\n")
	got := map[string]int{}
	for _, g := range gpus {
		got[g.Model] = g.VRAMMiB
	}
	require.Equal(t, map[string]int{
		"NVIDIA A100": 81920,
		"NVIDIA H100": 80 * 1024,
		"Old Card":    24 * 1024,
		"Odd":         0, // a sub-MiB accelerator does not exist: undetermined, not 0.5
		"Weird":       0,
		"Missing":     0,
		"Bare":        24564,
		"NoComma":     0,
	}, got)
	require.Equal(t, 0, parseMemFieldMiB("99999999999999999999 MiB"), "an overflowing figure is undetermined, not wrapped")
	require.Equal(t, 0, parseMemFieldMiB("   "))
}

func TestROCmOutputEdgeShapes(t *testing.T) {
	// A malformed index marker is skipped; a card line with no product falls back to a generic name.
	gpus := ParseROCmGPUs("GPU[0\nCard series\nCard model: \n")
	require.Equal(t, []LocalGPU{{Model: "AMD GPU"}, {Model: "AMD GPU"}}, gpus)
	require.Equal(t, "", rocmSeriesName("Card series Radeon"), "no colon, no name")
	require.Equal(t, "Radeon PRO W7900", rocmSeriesName("GPU[1] : Card model: Radeon PRO W7900"))

	for _, c := range []struct {
		out  string
		want int
	}{
		{"vram Total Memory (KB): 16777216", 16384},
		{"vram Total Memory (KiB): 1048576", 1024},
		{"vram Total Memory (MiB): 8192", 8192},
		{"vram Total Memory (GB): 16", 16384},
		{"vram Total Memory (GiB): 24", 24576},
		{"GPU[0] vram Total Memory (B): 17163091968\nGPU[1] vram Total Memory (B): 17163091968", 32736},
	} {
		total, ok := ParseROCmVRAMMiB(c.out)
		require.True(t, ok, c.out)
		require.Equal(t, c.want, total, c.out)
	}
	for _, bad := range []string{
		"vram Total Memory (MB) 8192", // unit but no colon
		"vram Total Memory (MB): lots",
		"vram Total Memory (MB): 0",
		"vram Total Memory (MB): -4",
		"vram Used Memory (MB): 100", // not a total
	} {
		_, ok := ParseROCmVRAMMiB(bad)
		require.False(t, ok, bad)
	}
}

func TestOllamaHostAcceptsEveryShapeTheVariableIsWrittenIn(t *testing.T) {
	for in, want := range map[string]string{
		":11434":                  "http://127.0.0.1:11434",
		"0.0.0.0:11434":           "http://0.0.0.0:11434",
		"gpu-box":                 "http://gpu-box",
		"https://ollama.lan:443":  "https://ollama.lan:443",
		"http://127.0.0.1:11434/": "http://127.0.0.1:11434/",
	} {
		require.Equal(t, want, ollamaHostURL(in), in)
	}
}

func TestQuantInNameIsTheSameParserTheDetectorUses(t *testing.T) {
	for name, want := range map[string]string{
		"Qwen3-8B-IQ1_M.gguf":              "IQ1_M",
		"/models/llama-3.1-8b.q4_k_m.gguf": "Q4_K_M",
		"README.md":                        "",
		"":                                 "",
	} {
		require.Equal(t, want, QuantInName(name), name)
		require.Equal(t, quantInName(name), QuantInName(name), name)
	}
}

// visionFromMeta reads only what a server REPORTS; anything else yields an empty answer rather
// than a guess, and every capability spelling a real server uses is recognised.
func TestVisionFromMetaReadsOnlyWhatTheServerReports(t *testing.T) {
	body := `{"data":[
		{"id":"llava","modalities":["text","image"]},
		{"id":"qwen-vl","vision":true},
		{"id":"gemma","supports_vision":true},
		{"id":"omni","multimodal":true},
		{"id":"plain","modalities":["text"]},
		{"modalities":["image"]}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok/models":
			require.Equal(t, "Bearer sk-local", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(body))
		case "/html/models":
			_, _ = w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got := visionFromMeta(srv.URL+"/ok", "sk-local")
	require.Equal(t, map[string]bool{"llava": true, "qwen-vl": true, "gemma": true, "omni": true}, got)
	require.Empty(t, visionFromMeta(srv.URL+"/html", ""), "a non-JSON 200 reports nothing")
	require.Empty(t, visionFromMeta(srv.URL+"/missing", ""), "a 404 reports nothing")
	require.Empty(t, visionFromMeta("http://127.0.0.1:1", ""), "an unreachable server reports nothing")
}

func TestAMalformedURLIsARequestErrorNotAPanic(t *testing.T) {
	_, err := authGet("http://bad host/models", "k")
	require.Error(t, err)
	_, err = authPost("http://bad host/models", "k", "application/json", strings.NewReader("{}"))
	require.Error(t, err)
}
