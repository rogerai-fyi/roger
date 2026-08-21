package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tool is one built-in capability the agent can call. Schema is the OpenAI
// function-tool definition advertised to the model; Run executes a parsed call in
// the sandbox rooted at root (the cwd by default). Mutating reports whether the call
// is side-effecting (write/exec) and therefore REQUIRES a confirm before Run; the
// read-only tools (read/list/fetch) auto-run. Keep this set SMALL and bounded.
type Tool struct {
	Name        string
	Description string
	// Mutating marks a side-effecting tool (write_file / run_shell). The loop shows a
	// y/N confirm for these before Run; a denied confirm returns a "user denied" result
	// to the model instead of running. Read-only tools auto-run.
	Mutating bool
	// Concurrent opts a tool into overlapping with its neighbours when the model queues
	// several calls at once (parallel.go). Only the BODY overlaps; the decision to run
	// and the recording of the result stay strictly ordered, so an overlapped batch
	// produces a byte-identical conversation to a serial one.
	//
	// Declaring it is a promise about Run: it must not touch state another call in the
	// same batch could be touching, and it must be safe to have several in flight. Every
	// Mutating tool is excluded by construction - a side-effecting call is a barrier -
	// so this is really a claim that a READ is independent of its siblings.
	Concurrent bool
	// Params is the JSON-schema "parameters" object for the OpenAI tool definition.
	Params map[string]any
	// Run executes the tool with the model-supplied args, sandboxed under root, and
	// returns the textual result fed back to the model. An error is also surfaced to
	// the model (as the tool result) so it can recover, not crash the loop. ctx is the
	// TURN's context: a tool that reaches the network or spawns a process must honor it,
	// so esc abandons work in flight instead of leaving the user waiting on it.
	Run func(ctx context.Context, root string, args map[string]any) (string, error)
}

// maxToolOutput caps a tool result fed back to the model so a huge file or command
// output can't blow the context (and the bill). Truncated results are marked. This is the
// ABSOLUTE ceiling; toolOutputBudget lowers it for a model whose window is too small to
// swallow it.
const maxToolOutput = 16 << 10 // 16 KiB

// The context-aware tool-output budget.
//
// THE INCIDENT (2026-08-07, Apple's on-device `foundation` band, 8192-token window): a
// single web_fetch returned ~10KB and the station answered "Exceeded model context window
// size". 16 KiB is a rounding error on a 128K band and HALF THE WINDOW on an 8K one, so a
// flat cap cannot be right for both. The budget scales with the window and is bounded on
// both sides:
//
//   - bytesPerToken is a deliberately CONSERVATIVE bytes-per-token estimate. Real English
//     runs ~4 bytes/token, but code, JSON and non-Latin scripts are denser, and guessing
//     high here is what caused the incident - so we assume the pessimistic 3.
//   - the share (1/4) leaves the other three quarters for the system prompt, the persona,
//     the conversation so far, and the model's own answer. A tool result that fills the
//     window leaves nothing to reason with.
//   - minToolOutput is the floor: below ~2 KiB a tool result is too mutilated to be worth
//     the call, so a very small band gets a usable slice rather than a useless sliver.
const (
	bytesPerToken      = 3
	toolOutputShareNum = 1
	toolOutputShareDen = 4
	minToolOutput      = 2 << 10 // 2 KiB
)

// ToolOutputBudget is toolOutputBudget for callers outside the package (the TUI sizes a
// Loop from the tuned band's reported context window).
func ToolOutputBudget(ctx int) int { return toolOutputBudget(ctx) }

// toolOutputBudget returns the byte cap for ONE tool result on a model with the given
// context window (in tokens). A ctx of 0 or less means "unknown" - the broker did not
// report one - and keeps the historical flat cap rather than guessing a smaller one.
func toolOutputBudget(ctx int) int {
	if ctx <= 0 {
		return maxToolOutput
	}
	b := ctx * bytesPerToken * toolOutputShareNum / toolOutputShareDen
	if b > maxToolOutput {
		return maxToolOutput
	}
	if b < minToolOutput {
		return minToolOutput
	}
	return b
}

// clipTo truncates s to budget bytes, marking the truncation so the model knows the result
// was cut and does not treat a partial file as complete. A budget of 0 or less means
// unbounded (the caller has no context information). It never splits a multi-byte rune -
// handing a model invalid UTF-8 corrupts the very text it is meant to read.
func clipTo(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	cut := budget
	// Walk forward off a continuation byte (10xxxxxx) to the next rune boundary, so the
	// kept prefix is always at least the budget and always valid UTF-8.
	for cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut++
	}
	return s[:cut] + "\n... (truncated)"
}

// shellTimeout bounds run_shell so a runaway command can't hang the turn. It is a
// var (defaulting to 60s) only so a test can shorten it to exercise the timeout
// branch; production behaviour is unchanged (the default is the real ceiling).
var shellTimeout = 60 * time.Second

// BuiltinTools returns the small, bounded toolset, in a stable order. Read-only
// tools (read_file, list_dir, web_fetch, and web_search when a provider is configured)
// auto-run; mutating tools (write_file, run_shell) are confirm-gated by the loop. The
// filesystem tools are sandboxed to root via resolveInRoot; web_fetch reaches the network
// through the fetch.go guard (read-only, text only).
func BuiltinTools() []Tool {
	tools := []Tool{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file in the working directory and return its contents. Read-only.",
			Mutating:    false,
			Concurrent:  true, // a read is independent of its siblings
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Path to the file, relative to the working directory."},
				},
				"required": []any{"path"},
			},
			Run: func(_ context.Context, root string, args map[string]any) (string, error) {
				p, err := resolveInRoot(root, str(args["path"]))
				if err != nil {
					return "", err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", err
				}
				return clip(string(b)), nil
			},
		},
		{
			Name:        "list_dir",
			Description: "List the entries of a directory in the working directory (default: the working directory itself). Read-only.",
			Mutating:    false,
			Concurrent:  true, // a read is independent of its siblings
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory path relative to the working directory. Defaults to '.'."},
				},
			},
			Run: func(_ context.Context, root string, args map[string]any) (string, error) {
				rel := str(args["path"])
				if strings.TrimSpace(rel) == "" {
					rel = "."
				}
				p, err := resolveInRoot(root, rel)
				if err != nil {
					return "", err
				}
				ents, err := os.ReadDir(p)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, e := range ents {
					name := e.Name()
					if e.IsDir() {
						name += "/"
					}
					b.WriteString(name)
					b.WriteByte('\n')
				}
				if b.Len() == 0 {
					return "(empty directory)", nil
				}
				return clip(b.String()), nil
			},
		},
		{
			Name:        "web_fetch",
			Description: "Fetch the text body of an http(s) URL and return it. Read-only; no JavaScript, text only.",
			Mutating:    false,
			Concurrent:  true, // a read is independent of its siblings
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "The http:// or https:// URL to fetch."},
				},
				"required": []any{"url"},
			},
			Run: func(ctx context.Context, _ string, args map[string]any) (string, error) {
				return webFetch(ctx, str(args["url"]))
			},
		},
		{
			Name:        "write_file",
			Description: "Write (create or overwrite) a UTF-8 text file in the working directory. Side-effecting: the user confirms before this runs.",
			Mutating:    true,
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Path to write, relative to the working directory."},
					"content": map[string]any{"type": "string", "description": "The full file contents to write."},
				},
				"required": []any{"path", "content"},
			},
			Run: func(_ context.Context, root string, args map[string]any) (string, error) {
				p, err := resolveInRoot(root, str(args["path"]))
				if err != nil {
					return "", err
				}
				content := str(args["content"])
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					return "", err
				}
				if err := os.WriteFile(p, []byte(content), 0644); err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %d bytes to %s", len(content), str(args["path"])), nil
			},
		},
		{
			Name:        "run_shell",
			Description: "Run a shell command in the working directory and return its combined output. Side-effecting: the user confirms before this runs. NOT sandboxed - an approved command can reach outside the working directory, so keep it minimal.",
			Mutating:    true,
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{"type": "string", "description": "The shell command line to run."},
				},
				"required": []any{"cmd"},
			},
			Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
				return runShell(ctx, root, str(args["cmd"]))
			},
		},
	}
	// web_search rides ONLY when a provider is configured: advertising it otherwise would
	// offer the model a tool that can only dead-end (features/answers/web_search.feature).
	if cfg, ok := loadSearchConfig(); ok {
		tools = append(tools, searchTool(cfg))
	}
	return tools
}

// ToolSchemas renders the toolset as the OpenAI `tools` array sent in the request
// body (each entry is {"type":"function","function":{name,description,parameters}}).
func ToolSchemas(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Params,
			},
		})
	}
	return out
}

// resolveInRoot joins rel onto root and verifies the result stays INSIDE root - the
// cwd sandbox. It rejects absolute paths and any "../" escape so a tool call can
// never read or write outside the directory the agent was opened in. root is
// cleaned/abs'd by the caller (the loop) once at startup.
func resolveInRoot(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths are not allowed (sandboxed to the working directory): %s", rel)
	}
	p := filepath.Clean(filepath.Join(root, rel))
	// Guard against "../" escapes: the cleaned path must be root or a descendant.
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the working directory sandbox: %s", rel)
	}
	return p, nil
}

// runShell runs cmd via the platform shell in root (c.Dir = root sets only the working
// directory), with a bounded timeout, and returns the combined stdout+stderr (clipped).
// It is only reached AFTER the loop's y/N confirm, so this never auto-runs. NOTE: this is
// NOT a sandbox - c.Dir only sets the cwd; an approved command can still read/write outside
// root (e.g. via an absolute path). The confirm gate (showing the literal user command,
// not this internal shell wrapper) is the real control here; the persona/UI copy must not
// imply run_shell is sandboxed.
func runShell(ctx context.Context, root, cmd string) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		return "", errors.New("empty command")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()
	c := shellCommand(ctx, cmd)
	c.Dir = root
	out, err := c.CombinedOutput()
	res := clip(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return res + fmt.Sprintf("\n(timed out after %s)", shellTimeout), nil
	}
	if err != nil {
		if res == "" {
			return "", err
		}
		return res + "\n(exit: " + err.Error() + ")", nil
	}
	if res == "" {
		return "(no output)", nil
	}
	return res, nil
}

// clip truncates s to maxToolOutput, marking a truncation so the model knows the
// result was cut (and doesn't treat a partial file as complete).
func clip(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + "\n... (truncated)"
}

// str coerces an arbitrary JSON-decoded arg to a string (the model sometimes sends a
// number or bool where a string is expected). nil -> "".
func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
