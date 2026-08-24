package tower

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// receiptsFile is the standalone plane's local receipt log: one JSON object per line,
// append-only. It is kept OUT of the bootstrap snapshot on purpose - receipts are
// high-volume bookkeeping, and rewriting the security-sensitive admission state on every
// request (to append one row) would be both slow and needless risk. A plant that wants to
// prune the log deletes the file; nothing depends on a receipt after it is written.
const receiptsFile = "receipts.jsonl"

// receiptsMu serialises appends within a process so two concurrent completions cannot
// interleave a partial line. Cross-process exclusion is the identity-directory lock the
// serving plane already holds.
var receiptsMu sync.Mutex

// RecordReceipt writes a free, locally-accounted receipt for one served request and returns
// it. It is bookkeeping, never billing: Cost is always zero and nothing here accrues, settles,
// or converts to RogerAI credit. Standalone only - a joined Tower's requests are receipted by
// Roger Core.
func (s *State) RecordReceipt(clientKeyHash, stationID, model string) (LocalReceipt, error) {
	if s.Mode != ModeStandalone {
		return LocalReceipt{}, ErrNotStandalone
	}
	reqID, err := randomHex(8)
	if err != nil {
		return LocalReceipt{}, err
	}
	fp, err := s.rootFingerprint()
	if err != nil {
		return LocalReceipt{}, err
	}
	rec := LocalReceipt{
		RequestID:       reqID,
		ClientKeyHash:   clientKeyHash,
		StationID:       stationID,
		Model:           model,
		NetworkID:       s.LocalNetworkID,
		RootFingerprint: fp,
		Cost:            0, // free and locally accounted, always
		At:              time.Now().Unix(),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return LocalReceipt{}, err
	}
	receiptsMu.Lock()
	defer receiptsMu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, receiptsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return LocalReceipt{}, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return LocalReceipt{}, err
	}
	return rec, nil
}

// Receipts returns the recorded receipts in the order they were written. A positive limit
// returns only the most recent that many (still in order); a limit <= 0 returns all. A
// missing log is an empty result, not an error - a network that has served nothing has no
// receipts, which is not a failure.
func (s *State) Receipts(limit int) ([]LocalReceipt, error) {
	receiptsMu.Lock()
	defer receiptsMu.Unlock()
	f, err := os.Open(filepath.Join(s.dir, receiptsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []LocalReceipt
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec LocalReceipt
		if json.Unmarshal(line, &rec) != nil {
			continue // a torn or corrupt line is skipped, not fatal to reading the rest
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
