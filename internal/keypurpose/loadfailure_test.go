package keypurpose

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// "Key loading failure blocks only safe behavior" - key_separation.feature.
//
// The spec's table has 73 rows. Its <result> column describes what each failure stops at a
// higher layer ("no new joined job can be issued"), which belongs to the component that
// consumes the purpose. What the KEYRING owes every one of those rows is the second half
// of the scenario, and it is the half that matters here:
//
//	it never silently generates a new production authority or falls back to another
//	role's key.
//
// That is asserted below for every in-scope row and every failure mode, rather than for
// the single row an earlier version of this suite happened to exercise.

// loadFailureRows reads the key column of the spec's failure table.
func loadFailureRows(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(specFile)
	require.NoError(t, err)
	defer f.Close()

	var rows []string
	state := "before"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "Scenario Outline: Key loading failure blocks only safe behavior"):
			state = "seeking-header"
		case state == "seeking-header" && strings.HasPrefix(line, "| key"):
			state = "in-table"
		case state == "in-table" && strings.HasPrefix(line, "|"):
			rows = append(rows, strings.TrimSpace(strings.Split(strings.Trim(line, "|"), "|")[0]))
		case state == "in-table" && strings.HasPrefix(line, "Scenario"):
			state = "done"
		}
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, rows)
	return rows
}

// specRowAliases maps the failure table's wording to the role table's wording. The two
// tables in the approved spec name a few roles slightly differently; rather than paper
// over that silently, every difference is written down here so it is reviewable.
var specRowAliases = map[string]Purpose{
	"Roger Core TLS identity":         PurposeRogerCoreTLSServiceIdentity,
	"Tower certificate issuer":        PurposeTowerCertificateIssuer,
	"Station admission/origin signer": PurposeStationAdmissionOriginSigner,
}

// standaloneRowPurposes maps the failure table's standalone rows onto the standalone
// realm's roles.
//
// FOUNDER RULING 2026-08-03: nothing is out of scope if it improves security. These rows
// were previously skipped as "a later phase"; a standalone Tower is a real network people
// run, and a load failure there fails open just as badly as one on the public side.
var standaloneRowPurposes = map[string]Purpose{
	"standalone pinned offline root":                        PurposeStandalonePinnedOfflineRoot,
	"standalone trust-document or trust-publication signer": PurposeStandaloneTrustDocument,
	"standalone policy signer":                              PurposeStandalonePolicy,
	"standalone client-admission signer":                    PurposeStandaloneClientAdmission,
	"standalone client-certificate issuer":                  PurposeStandaloneClientCertificate,
	"standalone local_bootstrap_verifier_authority signer":  PurposeStandaloneBootstrapVerifierAuth,
	"standalone local_operator_set signer":                  PurposeStandaloneOperatorSet,
	"standalone Station-admission signer":                   PurposeStandaloneStationAdmission,
	"standalone Station-certificate issuer":                 PurposeStandaloneStationCertificate,
	"standalone local_station_bridge_authority signer":      PurposeStandaloneBridgeAuthority,
	"standalone local_station_bridge_certificate issuer":    PurposeStandaloneBridgeCertificate,
	"standalone grant signer":                               PurposeStandaloneGrant,
	"standalone administrator-audit signer":                 PurposeStandaloneAdministratorAudit,
	"standalone receipt-ledger signer":                      PurposeStandaloneReceiptLedger,
	"standalone local_key_escrow_authorization signer":      PurposeStandaloneKeyEscrowAuthorization,
	"standalone local_key_escrow_result signer":             PurposeStandaloneKeyEscrowResult,
	"standalone bootstrap-verifier HMAC":                    PurposeStandaloneBootstrapVerifierHMAC,
	"standalone backup-encryption key":                      PurposeStandaloneBackupEncryption,
	"standalone local-service-TLS key":                      PurposeStandaloneTLS,
}

// outOfScopeRows are rows naming a key that Roger Core and a standalone Tower genuinely
// never hold - they belong to a Tower's or Station's own ring, which has its own realm and
// its own tests. Listed explicitly so "unmapped" always means someone needs to look.
var outOfScopeRows = map[string]string{
	"Tower identity key":   "a Tower-side key, covered by the joined-Tower realm",
	"Station identity key": "a Station-side key, covered by the Station realm",
}

// rowPurpose resolves one table row, or explains why it is out of scope.
func rowPurpose(row string) (Purpose, bool, string) {
	if why, ok := outOfScopeRows[row]; ok {
		return "", false, why
	}
	if p, ok := standaloneRowPurposes[row]; ok {
		return p, true, ""
	}
	if p, ok := specRowAliases[row]; ok {
		return p, true, ""
	}
	if p, ok := Lookup(row); ok {
		return p, true, ""
	}
	return "", false, ""
}

// Every row of the spec's table either names a Roger Core purpose or is explicitly
// declared out of scope. A row that matched neither would be a control nobody implemented
// and nobody noticed.
func TestEveryFailureTableRowIsAccountedFor(t *testing.T) {
	var unmapped []string
	inScope := 0
	for _, row := range loadFailureRows(t) {
		p, ok, why := rowPurpose(row)
		switch {
		case ok:
			require.True(t, Known(p), "row %q resolved to unknown purpose %q", row, p)
			inScope++
		case why != "":
			continue
		default:
			unmapped = append(unmapped, row)
		}
	}
	require.Empty(t, unmapped, "these spec rows name no known purpose and no stated reason")
	require.Greater(t, inScope, 70,
		"every row but the two Tower/Station ones is in scope; a lower count means a mapping was lost")
}

// The keyring's half of every row, for every failure mode the Given names.
func TestAnyLoadFailureFailsClosedWithoutMintingOrBorrowing(t *testing.T) {
	failures := []LoadFailure{
		LoadMissing, LoadMalformed, LoadUnreadable, LoadDuplicated, LoadUnavailable,
	}

	for _, row := range loadFailureRows(t) {
		p, ok, _ := rowPurpose(row)
		if !ok {
			continue
		}
		for _, mode := range failures {
			t.Run(row+"/"+string(mode), func(t *testing.T) {
				r, rErr := NewGeneratedRingFor(RealmOf(p))
				require.NoError(t, rErr)
				before := r.keyIDForTest(p)
				require.NoError(t, r.MarkUnloadable(p, mode))

				// Nothing signs or MACs under a failed role.
				_, err := r.use(p, []byte("work"))
				require.ErrorIs(t, err, ErrKeyUnavailable,
					"a %s key must stop the behavior that needs it", mode)
				require.Contains(t, err.Error(), string(mode),
					"the failure mode must be named, so an operator can fix the right thing")

				// It never silently generates a new production authority.
				require.Equal(t, before, r.keyIDForTest(p),
					"a failed role must not be quietly re-minted")

				// It never falls back to another role's key: no other role's signature
				// can satisfy this purpose.
				for _, other := range PurposesIn(r.Realm()) {
					if other == p || KindOf(other) != KindOf(p) {
						continue
					}
					sig, sErr := r.use(other, []byte("work"))
					if sErr != nil {
						continue
					}
					require.Error(t, r.check(p, []byte("work"), sig),
						"a %s key must not stand in for the failed %s", other, p)
					break
				}

				// And the failure is scoped: unrelated roles still work.
				for _, other := range PurposesIn(r.Realm()) {
					if other == p || KindOf(other) != KindOf(p) {
						continue
					}
					_, oErr := r.use(other, []byte("work"))
					require.NoError(t, oErr, "%s must be unaffected by a failure in %s", other, p)
					break
				}
			})
		}
	}
}

// A failed role must also stop the service at startup, not at its first signature.
func TestValidateReportsALoadFailureAndNamesIt(t *testing.T) {
	r := testRing(t)
	require.NoError(t, r.MarkUnloadable(PurposeSettlementSigner, LoadMalformed))

	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), string(PurposeSettlementSigner))
	require.Contains(t, err.Error(), string(LoadMalformed))
	requireNoSecrets(t, r, err.Error())
}

// A failure state cannot be cleared by rotating on top of it: recovering a role is an
// explicit act, not a side effect of asking for a new key.
func TestRotationDoesNotClearALoadFailure(t *testing.T) {
	r := testRing(t)
	require.NoError(t, r.MarkUnloadable(PurposeSettlementSigner, LoadUnreadable))

	_, err := r.Rotate(PurposeSettlementSigner, 0)
	require.Error(t, err, "a role in a failed state must be repaired explicitly, not rotated over")

	_, err = r.Sign(PurposeSettlementSigner, []byte("x"))
	require.ErrorIs(t, err, ErrKeyUnavailable)
}

func TestMarkUnloadableRejectsAnUnknownPurpose(t *testing.T) {
	r := testRing(t)
	require.ErrorIs(t, r.MarkUnloadable(Purpose("nope"), LoadMissing), ErrUnknownPurpose)
}
