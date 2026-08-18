// Package fleet is the FLEET-WIDE view of which Stations are routable right now.
//
// # WHY IT EXISTS
//
// A Tower holds ONE link, to ONE broker, and the inventory it pushes is accepted into that
// broker's memory. A request arriving at any other instance therefore could not see the
// Station at all, and fell back to "no node offers this model" - so with two brokers, a
// perfectly healthy Tower served roughly half the requests it should have. Nothing failed
// and nothing was logged; the capacity was simply invisible from the wrong door.
//
// This is the small, boring projection that fixes it: for each Tower, the leaves Core
// ACCEPTED, with the expiry the accepted revision already carries. It is a read model, not
// authority - the inventory itself, its signatures and its chain are decided by
// towercore/inv on the instance that received them, and nothing here re-decides any of it.
// A row is only ever a hint that a Station was routable a moment ago; every dispatch still
// re-checks the attachment before a grant is issued.
//
// # IT EXPIRES BY ITSELF
//
// Rows carry the accepted inventory's own expiry, so a Tower that vanishes without draining
// stops being routable on exactly the schedule the freshness window already promises. That
// is deliberately the same rule as the in-memory view rather than a second one to keep in
// step: a Tower is offered until its inventory ages out, wherever the question is asked.
package fleet

import "time"

// Station is one routable offer, as an instance that does not hold the link sees it.
type Station struct {
	TowerID   string
	StationID string
	OfferID   string
	Model     string
	Modality  string
	Capacity  int64
	Expires   time.Time
	// Endpoint is where CONSUMERS reach this Tower's data plane, host:port, as the Tower
	// advertised on its link. Empty means this Tower relays nothing, and its Stations are
	// reachable only on the Core-relayed path - a row without an endpoint is simply never
	// offered to an edge consumer.
	Endpoint string
	// NodeID is the BROKER node id of the same machine, copied from the attachment (M0 of
	// docs/relay-selection-design.md). It is what makes a candidate rankable: reliability,
	// probe outcomes and in-flight load are all recorded against the node id, and nothing
	// else on this row can reach them. Empty means unmeasured, which placement must treat as
	// "no evidence" rather than as "bad".
	NodeID string
	// PriceIn and PriceOut are what the consumer pays, in MICRO-USD PER 1,000,000 TOKENS -
	// copied verbatim from the Station's signed, band-checked inventory leaf so authorize can
	// pin them into the grant (Option C per-token billing). 0/0 means the offer is unpriced
	// and the byte tariff governs.
	PriceIn  int64
	PriceOut int64
}

// Store is the projection.
type Store interface {
	// Replace makes these rows THE routable set for a Tower, atomically.
	//
	// Replace rather than upsert-and-prune: an inventory revision is a complete statement of
	// what a Tower is offering, so anything not in it is no longer offered. Merging would
	// leave a withdrawn Station routable until something else happened to notice.
	Replace(towerID string, rows []Station) error
	// Candidates returns every unexpired row for a model.
	Candidates(model string, now time.Time) ([]Station, error)
	// Forget drops a Tower's rows, for a drain: the point of draining is that the fleet stops
	// being offered AT ONCE rather than aging out over the freshness window.
	Forget(towerID string) error
	// Reap drops expired rows.
	Reap(now time.Time) (int64, error)
	// RoutableTowers is the distinct Towers that have at least one unexpired row with a data
	// plane, so Core can canary each one that could actually carry an edge attempt. A Tower
	// with routable Stations but no endpoint is not listed: there is nothing to probe.
	RoutableTowers(now time.Time) ([]string, error)
	// ByTower is a Tower's own unexpired rows, so a canary can find a Station to probe behind
	// exactly that Tower without depending on which instance holds its link.
	ByTower(towerID string, now time.Time) ([]Station, error)
}
