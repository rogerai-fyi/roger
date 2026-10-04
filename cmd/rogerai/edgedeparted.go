package main

// THE SHORT RECORD A FORGOTTEN DEVICE LEAVES (features/edge/node_identity.feature, RE-ADMITTING A
// FORGOTTEN DEVICE). It sits beside edge.json, owner-readable only, and holds per device the name it
// had, what it is and when it was forgotten - nothing else. It is kept apart from edge.json because
// that file is the fleet, merged between processes field by field; a forgotten device is exactly
// what the fleet no longer holds.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

func edgeDepartedPath() string {
	return filepath.Join(filepath.Dir(configPath()), "edge-departed.json")
}

type edgeDepartedFile struct {
	Account  string          `json:"account"`
	Departed []edge.Departed `json:"departed,omitempty"`
}

// edgeLoadDeparted is this account's record of forgotten devices. A record that cannot be read is
// said, not shown as empty; one belonging to another account is not this Edge's to show.
func edgeLoadDeparted() ([]edge.Departed, error) {
	b, err := os.ReadFile(edgeDepartedPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not read the record of forgotten devices: %w", err)
	}
	var f edgeDepartedFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("could not read the record of forgotten devices: %w", err)
	}
	if f.Account != edgeAccount() {
		return nil, nil
	}
	return f.Departed, nil
}

// edgeSaveDeparted writes the record whole, under the same kind of lock edge.json takes.
func edgeSaveDeparted(list []edge.Departed) error {
	unlock, err := edge.FileLock(edgeDepartedPath())
	if err != nil {
		return err
	}
	defer unlock()
	return edgeWriteDeparted(list)
}

func edgeWriteDeparted(list []edge.Departed) error {
	b, err := json.MarshalIndent(edgeDepartedFile{Account: edgeAccount(), Departed: list}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteConfig(edgeDepartedPath(), b)
}

// edgeRecordDeparted keeps the short record for a device about to be forgotten. Every forget path
// calls it BEFORE anything is taken away, so a record that could not be written stops the forget
// rather than leaving a device gone with no way to bring it back by name. A record that was
// unreadable is started afresh: it is a convenience, and it must not stand in the way of a forget.
func edgeRecordDeparted(n store.EdgeNode) error {
	unlock, err := edge.FileLock(edgeDepartedPath())
	if err != nil {
		return err
	}
	defer unlock()
	list, err := edgeLoadDeparted()
	if err != nil {
		list = nil
	}
	return edgeWriteDeparted(edge.RecordDeparted(list, edge.DepartedOf(n, time.Now())))
}

// edgeReturningName is the name a re-admitted device comes back under: the one it had.
func edgeReturningName(id string) string {
	if list, err := edgeLoadDeparted(); err == nil {
		if name := edge.DepartedName(list, id); name != "" {
			return name
		}
	}
	// A member this authority enrolled is first seen under the name it enrolled as: the request was
	// signed by a key the owner allowed, so the name is the owner's choice (enrollment.feature). The
	// fleet still refuses one that is unusable or held by another member.
	if local, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir()); err == nil && isAuthority {
		return local.EnrolledName(id)
	}
	return ""
}

// edgeDepartedLabel is how a device not in the fleet is said, and the argument that re-admits it:
// the name it had when there is a record of one; else edgeDisplayName and the id as an argument.
func edgeDepartedLabel(id string) (said, arg string) {
	if list, err := edgeLoadDeparted(); err == nil {
		if name := edge.DepartedName(list, id); name != "" {
			return name, name
		}
	}
	return edgeDisplayName(id), id
}
