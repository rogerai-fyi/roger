package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	// Params is the JSON-schema "parameters" object for the OpenAI tool definition.
	Params map[string]any
	// Run executes the tool with the model-supplied args, sandboxed under root, and
	// returns the textual result fed back to the model. An error is also surfaced to
	// the model (as the tool result) so it can recover, not crash the loop.
	Run func(root string, args map[string]any) (string, error)
}

// maxToolOutput caps a tool result fed back to the model so a huge file or command
// output can't blow the context (and the bill). Truncated results are marked.
const maxToolOutput = 16 << 10 // 16 KiB

// maxFetchBytes caps a web_fetch body read.
const maxFetchBytes = 256 << 10

// fetchTimeout bounds web_fetch so a slow URL can't hang the turn.
const fetchTimeout = 20 * time.Second

// shellTimeout bounds run_shell so a runaway command can't hang the turn.
const shellTimeout = 60 * time.Second

// BuiltinTools returns the small, bounded toolset, in a stable order. Read-only
// tools (read_file, list_dir, web_fetch) auto-run; mutating tools (write_file,
// run_shell) are confirm-gated by the loop. The filesystem tools are sandboxed to
// root via resolveInRoot; web_fetch reaches the network (read-only, text only).
func BuiltinTools() []Tool {
	return []Tool{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file in the working directory and return its contents. Read-only.",
			Mutating:    false,
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Path to the file, relative to the working directory."},
				},
				"required": []any{"path"},
			},
			Run: func(root string, args map[string]any) (string, error) {
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
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory path relative to the working directory. Defaults to '.'."},
				},
			},
			Run: func(root string, args map[string]any) (string, error) {
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
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "The http:// or https:// URL to fetch."},
				},
				"required": []any{"url"},
			},
			Run: func(_ string, args map[string]any) (string, error) {
				return webFetch(str(args["url"]))
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
			Run: func(root string, args map[string]any) (string, error) {
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
			Run: func(root string, args map[string]any) (string, error) {
				return runShell(root, str(args["cmd"]))
			},
		},
	}
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
func runShell(root, cmd string) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		return "", errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellTimeout)
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

// webFetch GETs url and returns the clipped text body. http(s) only; bounded read +
// timeout. Read-only, so it auto-runs.
func webFetch(url string) (string, error) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("only http(s) URLs are supported: %q", url)
	}
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	body := clip(string(b))
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, body), nil
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Sprintf("HTTP %d (empty body)", resp.StatusCode), nil
	}
	return body, nil
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
