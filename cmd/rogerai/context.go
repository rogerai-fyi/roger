package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// context.go is the `roger context` verb group: portable signed context capsules
// (roger.context.v1). It is the FILE interop surface - export signs a capsule with the
// operator's existing identity, import verifies one (and can append-only merge it into a
// base thread), so a conversation moves across operators over a .rcap.json file
// (hermes/opencode) or a same-owner/local handoff. The encrypted stranger broker
// transport is a follow-on (ruling Q3). tool_calls are rejected at the boundary (Q1).

// contextExportedBy is the producer tag the CLI stamps into meta.exported_by. The app
// stamps "roger-ios"; the byte-parity golden covers both.
const contextExportedBy = "roger-cli"

// cmdContext routes `roger context export|import`.
func cmdContext(cfg config, args []string) error {
	if len(args) == 0 {
		contextUsage()
		return nil
	}
	switch args[0] {
	case "export":
		return cmdContextExport(args[1:])
	case "import":
		return cmdContextImport(args[1:])
	case "publish":
		return cmdContextPublish(cfg, args[1:])
	case "resolve":
		return cmdContextResolve(cfg, args[1:])
	case "-h", "--help", "help":
		contextUsage()
		return nil
	default:
		return fmt.Errorf("unknown context command %q (try export|import|publish|resolve)", args[0])
	}
}

// cmdContextExport signs a draft capsule (read from a file or stdin) into a portable
// signed .rcap.json (written to -o or stdout), using the operator's existing identity.
func cmdContextExport(args []string) error {
	fs := flag.NewFlagSet("context export", flag.ExitOnError)
	out := fs.String("o", "-", "output file (default: stdout)")
	fs.Usage = contextExportUsage
	inPath, rest := leadingPositional(args)
	fs.Parse(rest)
	if inPath == "" {
		inPath = fs.Arg(0)
	}
	in, closeIn, err := openIn(inPath)
	if err != nil {
		return err
	}
	defer closeIn()
	w, closeOut, err := openOut(*out)
	if err != nil {
		return err
	}
	defer closeOut()
	return contextExport(in, w, client.LoadOrCreateUserKey())
}

// cmdContextImport verifies a capsule (from a file or stdin). With --into it append-only
// merges the capsule into a base thread and writes the re-signed merged capsule; without
// it, it prints a one-line summary. A capsule whose signature does not verify is rejected.
func cmdContextImport(args []string) error {
	fs := flag.NewFlagSet("context import", flag.ExitOnError)
	into := fs.String("into", "", "base capsule to append-only merge the imported one into (.rcap.json)")
	out := fs.String("o", "-", "output file for the merged capsule (default: stdout; --into only)")
	fs.Usage = contextImportUsage
	inPath, rest := leadingPositional(args)
	fs.Parse(rest)
	if inPath == "" {
		inPath = fs.Arg(0)
	}
	in, closeIn, err := openIn(inPath)
	if err != nil {
		return err
	}
	defer closeIn()

	if *into == "" {
		return contextImportSummary(in, os.Stdout)
	}
	base, err := os.ReadFile(*into)
	if err != nil {
		return err
	}
	w, closeOut, err := openOut(*out)
	if err != nil {
		return err
	}
	defer closeOut()
	return contextImportMerge(in, base, w, client.LoadOrCreateUserKey())
}

// cmdContextPublish drives the ENCRYPTED STRANGER transport (Stage 3) from the CLI: it reads
// a signed capsule (.rcap.json, from a file or stdin), MINTS it to the broker's content-blind
// rendezvous under a FRESH one-time code (or --code), and prints the code for the DJ to hand
// to the guest out-of-band. The redaction floor is enforced (client.PublishStrangerCapsule
// refuses a non-summary capsule). The broker only ever stores {lookup, ciphertext}.
func cmdContextPublish(cfg config, args []string) error {
	fs := flag.NewFlagSet("context publish", flag.ExitOnError)
	code := fs.String("code", "", "one-time code to seal under (default: a fresh code, printed)")
	fs.Usage = contextPublishUsage
	inPath, rest := leadingPositional(args)
	fs.Parse(rest)
	if inPath == "" {
		inPath = fs.Arg(0)
	}
	if cfg.Broker == "" {
		return fmt.Errorf("no broker configured (set one up with `roger` first)")
	}
	in, closeIn, err := openIn(inPath)
	if err != nil {
		return err
	}
	defer closeIn()
	raw, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	// a fresh code REUSES the 40-bit RC/band tail (no new code format).
	full := *code
	if full == "" {
		full, _, _ = protocol.NewRCLinkCode()
	}
	if err := client.PublishStrangerCapsule(cfg.Broker, full, raw); err != nil {
		return err
	}
	fmt.Printf("published · hand this one-time code to the guest (expires in 10 min, single use):\n\n  %s\n\nthe guest runs: roger context resolve \"%s\"\n", full, full)
	return nil
}

// cmdContextResolve is the guest/receiver side: it RESOLVES the sealed capsule for a code from
// the broker (one-time, delete-on-read), OPENS it with the code, verifies the owner signature,
// and prints a summary - or with --into append-only merges it into a base thread. A gone/
// expired/wrong-code resolve is reported as such (the broker gives no existence oracle).
func cmdContextResolve(cfg config, args []string) error {
	fs := flag.NewFlagSet("context resolve", flag.ExitOnError)
	into := fs.String("into", "", "base capsule to append-only merge the resolved one into (.rcap.json)")
	out := fs.String("o", "-", "output file for the merged capsule (default: stdout; --into only)")
	fs.Usage = contextResolveUsage
	codeArg, rest := leadingPositional(args)
	fs.Parse(rest)
	if codeArg == "" {
		codeArg = fs.Arg(0)
	}
	if cfg.Broker == "" {
		return fmt.Errorf("no broker configured (set one up with `roger` first)")
	}
	if codeArg == "" {
		return fmt.Errorf("a one-time code is required (roger context resolve \"<code>\")")
	}
	raw, err := client.FetchCapsule(cfg.Broker, codeArg)
	if err != nil {
		return err
	}
	if *into == "" {
		return contextImportSummary(bytes.NewReader(raw), os.Stdout)
	}
	base, err := os.ReadFile(*into)
	if err != nil {
		return err
	}
	w, closeOut, err := openOut(*out)
	if err != nil {
		return err
	}
	defer closeOut()
	return contextImportMerge(bytes.NewReader(raw), base, w, client.LoadOrCreateUserKey())
}

// contextExport reads a draft capsule JSON from in, signs it with priv (stamping
// exported_by = the CLI producer, created_at = now), and writes the signed wire JSON.
func contextExport(in io.Reader, out io.Writer, priv ed25519.PrivateKey) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	var c capsule.Capsule
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("read draft: %w", err)
	}
	c.Capsule = capsule.Version // a draft may omit it; export always speaks the current version
	d := capsule.Draft{
		ID: c.ID, Thread: c.Thread, Redaction: c.Redaction,
		Summary: c.Summary, Memory: c.Memory, Messages: c.Messages, ToolsUsed: c.Meta.ToolsUsed,
	}
	signed, err := capsule.Export(d, priv, contextExportedBy, nil)
	if err != nil {
		return err
	}
	return writeCapsule(out, signed)
}

// contextImportSummary verifies the capsule in in and prints a one-line human summary. It
// returns an error (nothing written) when the capsule does not verify.
func contextImportSummary(in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	c, err := capsule.Import(data)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "verified capsule %s · %d turns · redaction=%s · owner=%s\n",
		c.ID, len(c.Messages), c.Redaction, short(c.Meta.OwnerPubkey))
	return nil
}

// contextImportMerge verifies the incoming capsule, append-only merges it into base, and
// writes the re-signed merged capsule. base is not re-verified (it is the operator's own
// thread); only the incoming capsule is.
func contextImportMerge(in io.Reader, base []byte, out io.Writer, priv ed25519.PrivateKey) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	incoming, err := capsule.Import(data)
	if err != nil {
		return err
	}
	var target capsule.Capsule
	if err := json.Unmarshal(base, &target); err != nil {
		return fmt.Errorf("read base: %w", err)
	}
	merged, err := capsule.Merge(incoming, target)
	if err != nil {
		return err
	}
	merged.Meta.ExportedBy = contextExportedBy
	merged.Sign(priv) // Merge clears the sig; the merged thread is ours, so re-sign it
	return writeCapsule(out, merged)
}

// writeCapsule marshals c and writes it with a trailing newline.
func writeCapsule(out io.Writer, c capsule.Capsule) error {
	raw, err := c.Marshal()
	if err != nil {
		return err
	}
	if _, err := out.Write(raw); err != nil {
		return err
	}
	_, err = out.Write([]byte("\n"))
	return err
}

// short trims a long hex key to a readable prefix for the summary line.
func short(hexKey string) string {
	if len(hexKey) <= 12 {
		return hexKey
	}
	return hexKey[:12] + "…"
}

// leadingPositional pulls a leading non-flag argument (the input file) out ahead of flag
// parsing, so `export draft.json -o out` works despite Go's flag stopping at the first
// positional (mirrors how cmdUse/cmdShare pull their positional first). When the first
// arg is a flag, the file is left for fs.Arg(0) after parsing (flags-first order).
func leadingPositional(args []string) (positional string, rest []string) {
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		return args[0], args[1:]
	}
	return "", args
}

// openIn opens path for reading, or returns stdin for "" / "-". The returned close is a
// no-op for stdin.
func openIn(path string) (io.Reader, func(), error) {
	if path == "" || path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// openOut opens path for writing (truncate), or returns stdout for "" / "-". The returned
// close is a no-op for stdout.
func openOut(path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

func contextExportUsage() {
	fmt.Print(`roger context export - sign a context capsule with your operator key

  roger context export draft.json -o convo.rcap.json   sign a draft into a portable capsule
  cat draft.json | roger context export                sign from stdin to stdout

The input is a roger.context.v1 draft (the capsule shape); export stamps exported_by,
created_at, and your owner_pubkey, then signs. tool_calls are not supported (they are
rejected at this boundary until their canonical form is pinned cross-language).
`)
}

func contextImportUsage() {
	fmt.Print(`roger context import - verify a context capsule (and optionally merge it)

  roger context import convo.rcap.json                    verify + print a summary
  roger context import guest.rcap.json --into mine.rcap.json -o merged.rcap.json

Import verifies the owner signature; a capsule that does not verify is rejected. With
--into, the imported turns are APPENDED (never replace/truncate) to the base thread and
the merged capsule is re-signed with your key.
`)
}

func contextPublishUsage() {
	fmt.Print(`roger context publish - hand a summary capsule to a stranger over the broker

  roger context publish convo.rcap.json               seal + mint under a fresh one-time code
  roger context publish convo.rcap.json --code "..."   seal + mint under a supplied code

The capsule is encrypted client-side under a one-time code and stored on the broker as an
opaque blob (the broker never sees the code, the key, or the plaintext). It must be
summary-only (a full capsule is refused). Hand the printed code to the guest out-of-band;
they run 'roger context resolve'. The blob is single-use and expires in 10 minutes.
`)
}

func contextResolveUsage() {
	fmt.Print(`roger context resolve - fetch + open a stranger capsule by its one-time code

  roger context resolve "147.520 MHz · 8F3K-9M2Q"                    verify + print a summary
  roger context resolve "<code>" --into mine.rcap.json -o merged.rcap.json

Resolve fetches the sealed blob ONCE (delete-on-read), opens it with the code, and verifies
the owner signature. With --into, the turns are APPENDED (never replace/truncate) to the base
thread and the merged capsule is re-signed with your key. A wrong/expired/used code is gone.
`)
}

func contextUsage() {
	fmt.Print(`roger context - carry a conversation across operators (roger.context.v1)

  roger context export draft.json -o convo.rcap.json   sign a portable context capsule
  roger context import convo.rcap.json                 verify a capsule + print a summary
  roger context import guest.rcap.json --into mine.rcap.json -o merged.rcap.json
  roger context publish convo.rcap.json                seal + mint to a stranger (one-time code)
  roger context resolve "<code>"                       fetch + open a stranger capsule

A capsule is a signed, portable snapshot of a thread. Import verifies the owner
signature and merges APPEND-ONLY (a handoff never erases context). publish/resolve carry it
encrypted over the broker's content-blind one-time-code rendezvous.
`)
}
