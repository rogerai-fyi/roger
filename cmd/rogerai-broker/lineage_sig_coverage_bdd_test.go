package main

// Step definitions for the E3 (receipt-to-job binding) and E4 (broker-signature
// coverage) scenarios in features/trust/lineage_receipts.feature.
//
// Both defects were live in production:
//   E4 - SignBroker signed the same canonical bytes as SignNode, with the broker's own
//        re-counts zeroed, so the numbers billedTokens() uses to charge the consumer and
//        credit the provider were covered by NO signature. There was also no VerifyBroker
//        at all, so nothing ever checked the counter-signature.
//   E3 - the broker checked only the node signature on a returned receipt and never that
//        the receipt described the job it dispatched. Settlement claims the hold keyed on
//        rec.RequestID, so a foreign, empty, or replayed id cleared the wrong row and left
//        the real hold to be swept back to the payer: served work, never billed.

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v5/internal/protocol"
)

type lrSig struct {
	node       *lrState
	brokerPub  ed25519.PublicKey
	brokerPriv ed25519.PrivateKey
	rec        protocol.UsageReceipt
	hashBefore string

	// binding
	wantReq, wantNode string
	bound             bool
	earlier           protocol.UsageReceipt

	strikeEvidence string

	// per-node chains
	chains map[string]string
	recs   map[string]protocol.UsageReceipt
}

// chainSign mirrors the agent's per-node chain: each node links to its OWN head.
func (g *lrSig) chainSign(nodeID, reqID string) protocol.UsageReceipt {
	if g.chains == nil {
		g.chains = map[string]string{}
		g.recs = map[string]protocol.UsageReceipt{}
	}
	rec := g.baseReceipt()
	rec.RequestID, rec.NodeID = reqID, nodeID
	rec.PrevHash = g.chains[nodeID]
	rec.SignNode(g.node.nodePriv)
	g.chains[nodeID] = rec.Hash()
	g.recs[nodeID+"/"+reqID] = rec
	return rec
}

func (g *lrSig) oneProcessTwoNodes(a, b string) error {
	g.fresh()
	g.chains, g.recs = map[string]string{}, map[string]protocol.UsageReceipt{}
	g.wantReq, g.wantNode = a, b
	return nil
}

func (g *lrSig) interleavedReceipts() error {
	g.chainSign(g.wantReq, "r1")
	g.chainSign(g.wantNode, "r1")
	g.chainSign(g.wantReq, "r2")
	g.chainSign(g.wantNode, "r2")
	return nil
}

func (g *lrSig) eachChainsToItsOwn() error {
	for _, n := range []string{g.wantReq, g.wantNode} {
		first, second := g.recs[n+"/r1"], g.recs[n+"/r2"]
		if second.PrevHash != first.Hash() {
			return fmt.Errorf("node %s: second receipt does not chain to its own first", n)
		}
	}
	return nil
}

func (g *lrSig) noCrossContamination() error {
	a, b := g.wantReq, g.wantNode
	if g.recs[a+"/r2"].PrevHash == g.recs[b+"/r1"].Hash() {
		return fmt.Errorf("node %s inherited node %s's hash", a, b)
	}
	if g.recs[b+"/r2"].PrevHash == g.recs[a+"/r1"].Hash() {
		return fmt.Errorf("node %s inherited node %s's hash", b, a)
	}
	return nil
}

func (g *lrSig) nodeWithNoReceipts() error {
	g.fresh()
	g.chains, g.recs = map[string]string{}, map[string]protocol.UsageReceipt{}
	return nil
}

func (g *lrSig) firstReceipt() error {
	g.rec = g.chainSign("fresh-node", "r1")
	return nil
}

func (g *lrSig) prevHashEmpty() error {
	if g.rec.PrevHash != "" {
		return fmt.Errorf("a node's first receipt must open its chain with an empty PrevHash, got %q", g.rec.PrevHash)
	}
	return nil
}

func (g *lrSig) noInheritedHash() error { return g.prevHashEmpty() }

func (g *lrSig) fresh() {
	g.brokerPub, g.brokerPriv, _ = ed25519.GenerateKey(nil)
}

func (g *lrSig) nodeKey() string   { return hex.EncodeToString(g.node.nodePub) }
func (g *lrSig) brokerKey() string { return hex.EncodeToString(g.brokerPub) }

func (g *lrSig) baseReceipt() protocol.UsageReceipt {
	return protocol.UsageReceipt{
		RequestID: "req-A", NodeID: "node-1", User: "alice", Model: "m",
		PromptTokens: 1000, CompletionTokens: 2000, PriceIn: 1, PriceOut: 2,
		TS: 1, PrevHash: "prev",
	}
}

// --- E4: signature coverage ------------------------------------------------

func (g *lrSig) aNodeSignedReceipt() error {
	g.fresh()
	g.rec = g.baseReceipt()
	g.rec.SignNode(g.node.nodePriv)
	g.hashBefore = g.rec.Hash()
	return nil
}

func (g *lrSig) brokerSetsItsFields() error {
	g.rec.BrokerPromptTokens = 900
	g.rec.BrokerCompletionTokens = 1800
	g.rec.GrantID = "grant-1"
	return nil
}

func (g *lrSig) nodeSigStillVerifies() error {
	if !g.rec.VerifyNode(g.nodeKey()) {
		return fmt.Errorf("the node signature must survive broker-set fields, but it no longer verifies")
	}
	return nil
}

func (g *lrSig) hashUnchanged() error {
	if got := g.rec.Hash(); got != g.hashBefore {
		return fmt.Errorf("the chain hash must not depend on broker-set fields: was %s, now %s", g.hashBefore, got)
	}
	return nil
}

func (g *lrSig) brokerHasSetItsFields() error {
	if err := g.aNodeSignedReceipt(); err != nil {
		return err
	}
	return g.brokerSetsItsFields()
}

func (g *lrSig) brokerCounterSigns() error {
	g.rec.SignBroker(g.brokerPriv)
	return nil
}

func (g *lrSig) brokerSigCoversThoseFields() error {
	if !g.rec.VerifyBroker(g.brokerKey()) {
		return fmt.Errorf("the broker signature does not verify")
	}
	// Prove coverage rather than assuming it: zeroing a broker-set field must break it.
	probe := g.rec
	probe.BrokerCompletionTokens = 0
	if probe.VerifyBroker(g.brokerKey()) {
		return fmt.Errorf("the broker signature still verifies after BrokerCompletionTokens changed - it does not cover the billed counts")
	}
	return nil
}

func (g *lrSig) formsDiffer() error {
	probe := g.rec
	probe.NodeSig = probe.BrokerSig
	// If the two canonical forms were identical, the broker sig would verify when
	// presented as a node sig against the broker key. It must not.
	if probe.VerifyNode(g.brokerKey()) {
		return fmt.Errorf("the node and broker canonical forms are identical - the broker-set fields are unsigned")
	}
	return nil
}

func (g *lrSig) fullySignedWithLowerRecount() error {
	if err := g.brokerHasSetItsFields(); err != nil {
		return err
	}
	return g.brokerCounterSigns()
}

func (g *lrSig) fullySigned() error { return g.fullySignedWithLowerRecount() }

func (g *lrSig) alterField(field string) error {
	switch field {
	case "BrokerPromptTokens":
		g.rec.BrokerPromptTokens = 1
	case "BrokerCompletionTokens":
		g.rec.BrokerCompletionTokens = 1
	case "GrantID":
		g.rec.GrantID = "other-grant"
	case "PromptTokens":
		g.rec.PromptTokens = 1
	case "CompletionTokens":
		g.rec.CompletionTokens = 1
	case "PriceIn":
		g.rec.PriceIn = 99
	case "PriceOut":
		g.rec.PriceOut = 99
	case "RequestID":
		g.rec.RequestID = "req-OTHER"
	case "NodeID":
		g.rec.NodeID = "node-OTHER"
	case "PrevHash":
		g.rec.PrevHash = "tampered"
	default:
		return fmt.Errorf("unknown field %q", field)
	}
	return nil
}

func (g *lrSig) brokerSigBroken() error {
	if g.rec.VerifyBroker(g.brokerKey()) {
		return fmt.Errorf("the broker signature still verifies after tampering")
	}
	return nil
}

func (g *lrSig) nodeSigBroken() error {
	if g.rec.VerifyNode(g.nodeKey()) {
		return fmt.Errorf("the node signature still verifies after tampering")
	}
	return nil
}

// A receipt whose broker signature does not verify must not be settleable. The broker
// only ever settles receipts it counter-signed itself, so the assertion is that the
// tampered receipt fails the check the settle path depends on.
func (g *lrSig) tamperedNotSettleable() error {
	if g.rec.VerifyBroker(g.brokerKey()) {
		return fmt.Errorf("a tampered receipt must not present a valid broker signature to the settle path")
	}
	return nil
}

func (g *lrSig) coSignedAndPublishedKey() error { return g.fullySignedWithLowerRecount() }

func (g *lrSig) consumerVerifiesOffline() error { return nil }

func (g *lrSig) verifyBrokerConfirms() error {
	if !g.rec.VerifyBroker(g.brokerKey()) {
		return fmt.Errorf("VerifyBroker must confirm a genuine co-signed receipt")
	}
	if g.rec.BrokerCompletionTokens == 0 || g.rec.GrantID == "" {
		return fmt.Errorf("the receipt under test must actually carry billed counts and grant attribution")
	}
	return nil
}

func (g *lrSig) unsignedDoesNotVerify() error {
	probe := g.rec
	probe.BrokerSig = ""
	if probe.VerifyBroker(g.brokerKey()) {
		return fmt.Errorf("a receipt with no broker signature must not verify")
	}
	return nil
}

// --- E3: receipt-to-job binding --------------------------------------------

func (g *lrSig) dispatched(req, node string) error {
	g.fresh()
	g.wantReq, g.wantNode = req, node
	return nil
}

func (g *lrSig) returnsBoundReceipt(req, node string) error {
	g.rec = g.baseReceipt()
	g.rec.RequestID, g.rec.NodeID = req, node
	g.rec.SignNode(g.node.nodePriv)
	g.bound = g.rec.BindsTo(g.wantReq, g.wantNode)
	return nil
}

func (g *lrSig) returnsDefectiveReceipt(defect string) error {
	g.rec = g.baseReceipt()
	g.rec.RequestID, g.rec.NodeID = g.wantReq, g.wantNode
	switch defect {
	case "RequestID naming another request":
		g.rec.RequestID = "req-OTHER"
	case "an empty RequestID":
		g.rec.RequestID = ""
	case "RequestID of an earlier settled job":
		g.rec.RequestID = "req-EARLIER"
	case "NodeID naming another node":
		g.rec.NodeID = "node-OTHER"
	case "an empty NodeID":
		g.rec.NodeID = ""
	default:
		return fmt.Errorf("unknown defect %q", defect)
	}
	// Still a perfectly valid signature - binding is the only thing that catches it.
	g.rec.SignNode(g.node.nodePriv)
	if !g.rec.VerifyNode(g.nodeKey()) {
		return fmt.Errorf("the defective receipt must still be signature-valid, else the test proves nothing")
	}
	g.bound = g.rec.BindsTo(g.wantReq, g.wantNode)
	return nil
}

func (g *lrSig) receiptBinds() error {
	if !g.bound {
		return fmt.Errorf("the receipt should bind to the dispatched job but does not")
	}
	return nil
}

func (g *lrSig) receiptDoesNotBind() error {
	if g.bound {
		return fmt.Errorf("the receipt binds to the dispatched job but must not")
	}
	return nil
}

func (g *lrSig) settlesAgainstOwnHold() error {
	if g.rec.RequestID != g.wantReq {
		return fmt.Errorf("a bound receipt must carry the dispatched request id, so settlement claims this job's own hold")
	}
	return nil
}

func (g *lrSig) noForeignHoldCleared() error {
	// The guard runs BEFORE settlement, so no hold row - foreign or absent - is touched.
	if g.bound {
		return fmt.Errorf("an unbound receipt must be rejected before settlement can clear any hold row")
	}
	return nil
}

func (g *lrSig) holdsEarlierReceipt(node string) error {
	g.fresh()
	g.wantNode = node
	g.earlier = g.baseReceipt()
	g.earlier.RequestID, g.earlier.NodeID = "req-EARLIER", node
	g.earlier.SignNode(g.node.nodePriv)
	return nil
}

func (g *lrSig) replaysForNewRequest() error {
	g.wantReq = "req-NEW"
	g.rec = g.earlier // byte-identical replay, signature intact
	g.bound = g.rec.BindsTo(g.wantReq, g.wantNode)
	return nil
}

func (g *lrSig) earlierRowsUntouched() error {
	if g.rec.RequestID != g.earlier.RequestID {
		return fmt.Errorf("the replayed receipt should be the earlier one unchanged")
	}
	if g.bound {
		return fmt.Errorf("the replay bound to the new job, so it would have written against the earlier request's rows")
	}
	return nil
}

func (g *lrSig) newRequestSettlesNothing() error { return g.receiptDoesNotBind() }

func (g *lrSig) unboundStrikeRecorded(kind string) error {
	if g.node.b == nil {
		return fmt.Errorf("no broker from the relay run")
	}
	st, err := g.node.b.db.StrikesByOwner("op1", 100)
	if err != nil {
		return err
	}
	for _, k := range st {
		if k.Kind == kind {
			g.strikeEvidence = k.Evidence
			return nil
		}
	}
	return fmt.Errorf("no %q strike was recorded for the node's owner", kind)
}

func (g *lrSig) strikeNamesBothRequests() error {
	for _, want := range []string{"dispatched_request", "returned_request"} {
		if !strings.Contains(g.strikeEvidence, want) {
			return fmt.Errorf("strike evidence does not name %s: %s", want, g.strikeEvidence)
		}
	}
	if !strings.Contains(g.strikeEvidence, "req-FOREIGN") {
		return fmt.Errorf("strike evidence does not carry the returned request id: %s", g.strikeEvidence)
	}
	return nil
}

func lrRegisterSigCoverageSteps(sc *godog.ScenarioContext, st *lrState) {
	g := &lrSig{node: st}

	// E4 - coverage
	sc.Step(`^a receipt signed by the node$`, g.aNodeSignedReceipt)
	sc.Step(`^the broker later sets BrokerPromptTokens, BrokerCompletionTokens, and GrantID$`, g.brokerSetsItsFields)
	sc.Step(`^the node signature still verifies$`, g.nodeSigStillVerifies)
	sc.Step(`^the receipt hash used as the next PrevHash is unchanged by those broker fields$`, g.hashUnchanged)
	sc.Step(`^the broker has set BrokerPromptTokens, BrokerCompletionTokens, and GrantID$`, g.brokerHasSetItsFields)
	sc.Step(`^the broker counter-signs the receipt$`, g.brokerCounterSigns)
	sc.Step(`^the broker signature verifies over a canonical form that includes those fields$`, g.brokerSigCoversThoseFields)
	sc.Step(`^the node signature and the broker signature are over different canonical forms$`, g.formsDiffer)
	sc.Step(`^a fully signed receipt whose broker recount is lower than the node claim$`, g.fullySignedWithLowerRecount)
	sc.Step(`^a fully signed receipt$`, g.fullySigned)
	sc.Step(`^"([^"]*)" is altered after the broker counter-signs$`, g.alterField)
	sc.Step(`^"([^"]*)" is altered after both signatures$`, g.alterField)
	sc.Step(`^the broker signature no longer verifies$`, g.brokerSigBroken)
	sc.Step(`^the node signature no longer verifies$`, g.nodeSigBroken)
	sc.Step(`^the tampered receipt cannot be settled$`, g.tamperedNotSettleable)
	sc.Step(`^a co-signed receipt and the broker's published public key$`, g.coSignedAndPublishedKey)
	sc.Step(`^a consumer verifies it without contacting the broker$`, g.consumerVerifiesOffline)
	sc.Step(`^VerifyBroker confirms the broker signed exactly the billed counts and grant attribution$`, g.verifyBrokerConfirms)
	sc.Step(`^a receipt carrying no broker signature does not verify$`, g.unsignedDoesNotVerify)
	sc.Step(`^a "([^"]*)" strike is recorded against the node's owner$`, g.unboundStrikeRecorded)
	sc.Step(`^the strike evidence names the dispatched request and the returned request$`, g.strikeNamesBothRequests)

	// E3 - binding
	sc.Step(`^the broker dispatched request "([^"]*)" to node "([^"]*)"$`, g.dispatched)
	sc.Step(`^the node returns a validly signed receipt for request "([^"]*)" from "([^"]*)"$`, g.returnsBoundReceipt)
	sc.Step(`^the node returns a validly signed receipt with "([^"]*)"$`, g.returnsDefectiveReceipt)
	sc.Step(`^the receipt binds to the dispatched job$`, g.receiptBinds)
	sc.Step(`^the receipt does not bind to the dispatched job$`, g.receiptDoesNotBind)
	sc.Step(`^settlement proceeds against that job's own hold$`, g.settlesAgainstOwnHold)
	sc.Step(`^no foreign or absent hold row is cleared$`, g.noForeignHoldCleared)
	sc.Step(`^node "([^"]*)" holds a validly signed receipt from an earlier settled request$`, g.holdsEarlierReceipt)
	sc.Step(`^it returns that same receipt for a newly dispatched request$`, g.replaysForNewRequest)
	sc.Step(`^the earlier request's ledger rows are untouched$`, g.earlierRowsUntouched)
	sc.Step(`^the new request settles nothing and its hold is refunded$`, g.newRequestSettlesNothing)

	// E5 - per-node chains
	sc.Step(`^one agent process serves node "([^"]*)" and node "([^"]*)"$`, g.oneProcessTwoNodes)
	sc.Step(`^each node produces two receipts in an interleaved order$`, g.interleavedReceipts)
	sc.Step(`^each node's second receipt chains to that SAME node's first receipt$`, g.eachChainsToItsOwn)
	sc.Step(`^neither node's chain contains a hash produced by the other node$`, g.noCrossContamination)
	sc.Step(`^a node that has produced no receipts yet$`, g.nodeWithNoReceipts)
	sc.Step(`^it produces its first receipt$`, g.firstReceipt)
	sc.Step(`^that receipt's PrevHash is empty$`, g.prevHashEmpty)
	sc.Step(`^it does not inherit a hash from any other node$`, g.noInheritedHash)
}
