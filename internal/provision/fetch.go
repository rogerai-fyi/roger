// Package provision acquires a large file and refuses to lie about it.
//
// It exists for model weights, which are gigabytes over somebody's home connection, and for the
// inference runtime itself. Both are named by features/serving/local_models.feature and the
// acquisition clauses of the approved features/share/model_catalog.feature; this is the piece
// both of them stand on.
//
// THE RULES, AND WHY EACH ONE IS STRUCTURAL RATHER THAN REMEMBERED:
//
//  1. NOTHING IS SERVABLE UNTIL IT IS VERIFIED. Bytes land at "<dest>.part" and are renamed to
//     dest only after the size and the digest both match. That is not a convention a future
//     caller can forget: there is no code path that writes to dest directly, so a truncated or
//     wrong file cannot appear at the name anything else reads.
//  2. A FAILURE SAYS WHICH FAILURE. "It did not work" is not something an owner can act on. No
//     space, no network, gated, missing, rate limited, corrupt and cancelled are seven different
//     problems with seven different next steps, and they are seven values here.
//  3. THE DISK IS CHECKED FIRST, BEFORE THE SERVER IS CONTACTED. Discovering at 94 per cent that
//     a 4 GB file will not fit wastes the download AND fills the disk on the way.
//  4. A RESUME EITHER RESUMES OR STARTS OVER, NEVER SOMETHING IN BETWEEN. A server that ignores
//     Range answers 200 with the whole body; appending that to what is on disk gives a file of
//     the right length made of the wrong bytes, whose digest then fails for a reason nobody can
//     diagnose from the outside. So the response status decides, not the request.
//  5. WHAT IT CANNOT MEASURE IT DOES NOT ENFORCE. A machine whose free space cannot be read is
//     not refused on a guess.
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// The reasons a fetch did not happen. They are values because the right next step differs for
// each: free some space, check the network, accept a licence, look for a different file, wait,
// or try again because what arrived was not what was promised.
var (
	ErrNoSpace     = errors.New("not enough free disk")
	ErrUnreachable = errors.New("the host did not answer")
	ErrGated       = errors.New("the source will not serve this without access")
	ErrNotFound    = errors.New("the source does not have this file")
	ErrRateLimited = errors.New("the source is rate limiting us")
	ErrUpstream    = errors.New("the source answered with an error")
	ErrCorrupt     = errors.New("what arrived is not what was promised")
)

// Request is one file to acquire. Bytes and SHA256 are REQUIRED, and that is the point: a fetch
// with nothing to verify against is a fetch that cannot tell success from a truncated stream, and
// this package would rather refuse than pretend.
type Request struct {
	URL    string // resolved fresh each attempt; a presigned CDN URL expires
	Dest   string // the final path. Nothing is written here until it is verified.
	Bytes  int64  // exact expected size
	SHA256 string // hex
	Header map[string]string
}

// Progress is what a screen needs to draw an honest bar: real byte counts, a rate and a time
// remaining, never a bare fraction. It is computed here so the terminal and the console cannot
// disagree about the same download.
type Progress struct {
	Done      int64
	Total     int64
	Rate      float64       // bytes per second over the life of this attempt
	Remaining time.Duration // an estimate, zero when it cannot be estimated yet
	Resumed   bool          // this attempt continued an earlier one
}

// Fetcher acquires files. The two seams exist so the rules above can be tested without a real
// disk and without a real network: FreeSpace reports what is available, Client does the request.
type Fetcher struct {
	Client     *http.Client
	FreeSpace  func(dir string) (int64, bool)
	OnProgress func(Progress)
	// SpaceMargin is how much more than the file itself must be free. A download needs room for
	// the partial and a little to spare; filling a card to the last byte is its own failure.
	SpaceMargin float64
}

const defaultSpaceMargin = 1.05

// progressEvery bounds how often a screen is told, so a fast local transfer does not spend its
// time formatting numbers.
const progressEvery = 250 * time.Millisecond

// Fetch acquires one file, resuming if it can, and returns nil only when the file at Dest is
// exactly what Request described.
func (f *Fetcher) Fetch(ctx context.Context, r Request) error {
	if r.URL == "" || r.Dest == "" || r.SHA256 == "" || r.Bytes <= 0 {
		return fmt.Errorf("a fetch needs a URL, a destination, a size and a digest: %w",
			errors.New("incomplete request"))
	}
	// ALREADY DONE is the commonest case on a second run, and it must cost nothing: no request,
	// no truncation, no re-download of four gigabytes because a process restarted.
	if ok, _ := fileMatches(r.Dest, r.Bytes, r.SHA256); ok {
		f.report(Progress{Done: r.Bytes, Total: r.Bytes})
		return nil
	}
	if err := f.checkSpace(r); err != nil {
		return err
	}
	part := r.Dest + ".part"
	have := partialSize(part, r.Bytes)

	resp, err := f.get(ctx, r, have)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// THE RESPONSE DECIDES WHETHER THIS IS A RESUME, NOT THE REQUEST. A 206 continues; a 200
	// means the server is sending from the top whatever we asked, so the partial is abandoned
	// rather than appended to.
	resume := have > 0 && resp.StatusCode == http.StatusPartialContent
	if !resume {
		have = 0
	}
	file, h, err := openPartial(part, have)
	if err != nil {
		return err
	}
	written, copyErr := f.stream(ctx, file, h, resp.Body, have, r.Bytes, resume)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr // the partial stays on disk: the next attempt resumes from it
	}
	if closeErr != nil {
		return closeErr
	}
	if err := verify(part, written, r.Bytes, hex.EncodeToString(h.Sum(nil)), r.SHA256); err != nil {
		return err
	}
	return os.Rename(part, r.Dest)
}

// checkSpace refuses before anything is written, and says the arithmetic rather than just no.
func (f *Fetcher) checkSpace(r Request) error {
	if f.FreeSpace == nil {
		return nil
	}
	free, ok := f.FreeSpace(dirOf(r.Dest))
	if !ok {
		return nil // unmeasurable is not the same as too small
	}
	margin := f.SpaceMargin
	if margin <= 0 {
		margin = defaultSpaceMargin
	}
	need := int64(float64(r.Bytes) * margin)
	if free < need {
		return fmt.Errorf("%w: it needs %s and %s is free on %s",
			ErrNoSpace, bytesWord(need), bytesWord(free), dirOf(r.Dest))
	}
	return nil
}

// get issues the request, translating every answer into one of the named reasons.
func (f *Fetcher) get(ctx context.Context, r Request, from int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
	}
	cl := f.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusPartialContent:
		return resp, nil
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		// A gated repository answers exactly this. It is the far end refusing to serve the bytes
		// to anybody without accepted access, which is a different thing from us refusing.
		reason := resp.Header.Get("X-Error-Code")
		if reason == "" {
			reason = "access is required"
		}
		return nil, fmt.Errorf("%w (%s)", ErrGated, reason)
	case http.StatusNotFound, http.StatusGone:
		return nil, ErrNotFound
	case http.StatusTooManyRequests:
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			return nil, fmt.Errorf("%w: try again in %ss", ErrRateLimited, ra)
		}
		return nil, ErrRateLimited
	case http.StatusRequestedRangeNotSatisfiable:
		// What is on disk is at least as long as the file: it is not a resume, it is a mismatch.
		return nil, fmt.Errorf("%w: the source says there is nothing after byte %d", ErrCorrupt, from)
	}
	return nil, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
}

// stream copies the body, hashing as it goes and reporting progress on a bounded cadence.
func (f *Fetcher) stream(ctx context.Context, w io.Writer, h io.Writer, body io.Reader,
	have, total int64, resumed bool) (int64, error) {
	started := time.Now()
	done := have
	last := time.Time{}
	f.report(Progress{Done: done, Total: total, Resumed: resumed})
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return done, werr
			}
			if _, werr := h.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if time.Since(last) >= progressEvery {
				last = time.Now()
				f.report(progressAt(done, total, have, started, resumed))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			return done, fmt.Errorf("%w: %v", ErrUnreachable, rerr)
		}
	}
	f.report(progressAt(done, total, have, started, resumed))
	return done, nil
}

// progressAt is the rate and the remaining time, measured over THIS attempt only: bytes that were
// already on disk were not transferred now and counting them would report a rate nobody achieved.
func progressAt(done, total, have int64, started time.Time, resumed bool) Progress {
	p := Progress{Done: done, Total: total, Resumed: resumed}
	moved := done - have
	elapsed := time.Since(started).Seconds()
	if moved > 0 && elapsed > 0 {
		p.Rate = float64(moved) / elapsed
		if left := total - done; left > 0 {
			p.Remaining = time.Duration(float64(left)/p.Rate) * time.Second
		}
	}
	return p
}

func (f *Fetcher) report(p Progress) {
	if f.OnProgress != nil {
		f.OnProgress(p)
	}
}

// openPartial opens the partial for appending at have, hashing what is already there so the
// digest covers the whole file and not only this attempt's share of it.
func openPartial(part string, have int64) (*os.File, interface {
	io.Writer
	Sum([]byte) []byte
}, error) {
	h := sha256.New()
	if have == 0 {
		f, err := os.Create(part)
		return f, h, err
	}
	// Re-read what is on disk into the hash. It costs one pass over the partial and it is the
	// only way a resumed file can be verified as a whole.
	existing, err := os.Open(part)
	if err != nil {
		return nil, nil, err
	}
	if _, err := io.CopyN(h, existing, have); err != nil {
		existing.Close()
		return nil, nil, err
	}
	existing.Close()
	f, err := os.OpenFile(part, os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, h, nil
}

// partialSize is how much of an earlier attempt is usable. A partial at or beyond the expected
// size is not a resume point, it is rubbish, and starting over is the honest response.
func partialSize(part string, total int64) int64 {
	fi, err := os.Stat(part)
	if err != nil || fi.Size() <= 0 || fi.Size() >= total {
		return 0
	}
	return fi.Size()
}

// verify is the gate between "bytes arrived" and "there is a file here". Size is checked first
// because it is free and catches the common truncation without hashing gigabytes.
func verify(part string, written, want int64, got, wantSum string) error {
	if written != want {
		return fmt.Errorf("%w: %s arrived, %s expected", ErrCorrupt, bytesWord(written), bytesWord(want))
	}
	if got != wantSum {
		return fmt.Errorf("%w: digest %s, expected %s", ErrCorrupt, short(got), short(wantSum))
	}
	return nil
}

// fileMatches reports whether the file already there is exactly what was asked for.
func fileMatches(path string, size int64, wantSum string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() != size {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == wantSum, nil
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			if i == 0 {
				return path[:1]
			}
			return path[:i]
		}
	}
	return "."
}

// bytesWord is a size as a person reads it. Sizes here are always large, so one decimal on a
// binary unit is the useful precision; exact byte counts belong in a log, not a sentence.
func bytesWord(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	// PiB is IN the list, not the fallback after it. Left out, one pebibyte printed as
	// "1024.0 PiB" - the loop had already divided for TiB and then fell through with the unit
	// label of the next step up. Caught by a table test at the exact boundary.
	v, units := float64(n), []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, u := range units {
		v /= unit
		if v < unit {
			return strconv.FormatFloat(v, 'f', 1, 64) + " " + u
		}
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " EiB"
}

// The small helpers are exported for the package's own tests, which pin their edges directly:
// a size word that reads wrong or a digest sliced past its end is the kind of detail that
// survives review and then appears in front of an owner.
func BytesWord(n int64) string      { return bytesWord(n) }
func ShortDigest(sum string) string { return short(sum) }
func DirOf(path string) string      { return dirOf(path) }
