package main

// Makes features/auth/email_link.feature executable: each scenario names its pinning test(s)
// (a Go test in this package, or a web/test/*.test.mjs run through node, skipped - not faked -
// when node is absent). The scenario passes only if every named test passes.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

var linkTests = map[string]func(*testing.T){
	"TestAddingAnAddressRecordsItVerifiedOnThisAccountOnly":              TestAddingAnAddressRecordsItVerifiedOnThisAccountOnly,
	"TestAfterAddingTheEmailedCodeReachesTheSameAccount":                 TestAfterAddingTheEmailedCodeReachesTheSameAccount,
	"TestAnAddressVerifiedOnAnotherAccountIsRefusedWithoutNamingIt":      TestAnAddressVerifiedOnAnotherAccountIsRefusedWithoutNamingIt,
	"TestAddingTheSameAddressAgainIsIdempotent":                          TestAddingTheSameAddressAgainIsIdempotent,
	"TestReplacingTheAddressDropsTheOldProof":                            TestReplacingTheAddressDropsTheOldProof,
	"TestAWrongSpentOrExpiredCodeIsRefusedUniformlyAndNothingIsRecorded": TestAWrongSpentOrExpiredCodeIsRefusedUniformlyAndNothingIsRecorded,
	"TestAnExpiredTokenIsRefused":                                        TestAnExpiredTokenIsRefused,
	"TestATokenFromAnotherAccountOrForAnotherAddressOrTamperedIsRefused": TestATokenFromAnotherAccountOrForAnotherAddressOrTamperedIsRefused,
	"TestLinkRequiresASignedInSessionAndMailsNothingWithout":             TestLinkRequiresASignedInSessionAndMailsNothingWithout,
	"TestLinkRequiresTheWebOrigin":                                       TestLinkRequiresTheWebOrigin,
	"TestASessionWithNoOwnerRowCannotLinkAndNothingIsMailed":             TestASessionWithNoOwnerRowCannotLinkAndNothingIsMailed,
	"TestADeletedAccountCannotLink":                                      TestADeletedAccountCannotLink,
	"TestAnEmailSessionCannotAddAnotherAddress":                          TestAnEmailSessionCannotAddAnotherAddress,
	"TestLinkRequestsAreRateLimited":                                     TestLinkRequestsAreRateLimited,
	"TestLinkNeverLogsTheAddressOrTheCode":                               TestLinkNeverLogsTheAddressOrTheCode,
	"TestLinkTellsTheOwnerWhenASeparateEmailWalletHoldsAFunds":           TestLinkTellsTheOwnerWhenASeparateEmailWalletHoldsAFunds,
	"TestLinkSaysNothingAboutMergingWhenThereIsNoSeparateWallet":         TestLinkSaysNothingAboutMergingWhenThereIsNoSeparateWallet,
	"TestALinkTokenCanNeverBeReplayedAsASessionCookie":                   TestALinkTokenCanNeverBeReplayedAsASessionCookie,
	"TestASessionWithAnUnparseableGitHubIdIsRejected":                    TestASessionWithAnUnparseableGitHubIdIsRejected,
	"TestALiveEmailSessionIsNotAMixedIdentityAfterTheAddressIsLinked":    TestALiveEmailSessionIsNotAMixedIdentityAfterTheAddressIsLinked,
	"TestAnAddressWithAPipeCanBeLinked":                                  TestAnAddressWithAPipeCanBeLinked,
	"TestTheLinkBudgetIsPerAccountNotPerIP":                              TestTheLinkBudgetIsPerAccountNotPerIP,
	"TestLinkingNeverMergesWalletsOrBalances":                            TestLinkingNeverMergesWalletsOrBalances,
}

var linkTestRe = regexp.MustCompile(`# test: (\S+)`)

func TestEmailLinkBDD(t *testing.T) {
	feature, err := os.ReadFile("../../features/auth/email_link.feature")
	if err != nil {
		t.Fatal(err)
	}
	var perScenario [][]string
	for _, block := range strings.Split(string(feature), "  Scenario:")[1:] {
		m := linkTestRe.FindStringSubmatch(block)
		if m == nil {
			t.Fatalf("a scenario names no pinning test: %.60s", block)
		}
		perScenario = append(perScenario, strings.Split(m[1], ","))
	}
	idx := -1
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { idx++; return ctx, nil })
			// Every step asserts the scenario's pinning tests pass (re-running them per step is
			// cheap: they are in-memory; node suites run once per step but are tiny).
			sc.Step(`^.*$`, func() error {
				for _, name := range perScenario[idx] {
					if strings.HasSuffix(name, ".mjs") {
						node, err := exec.LookPath("node")
						if err != nil {
							return godog.ErrSkip
						}
						if out, err := exec.Command(node, "--test", "../../"+name).CombinedOutput(); err != nil {
							return fmt.Errorf("%s failed:\n%s", name, out)
						}
						continue
					}
					fn, ok := linkTests[name]
					if !ok {
						return fmt.Errorf("scenario names %q but no such test is registered", name)
					}
					if !t.Run(name, fn) {
						return fmt.Errorf("%s failed", name)
					}
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/auth/email_link.feature"}, TestingT: t, Output: os.Stdout},
	}
	if suite.Run() != 0 {
		t.Fatal("email-link scenarios failed (see godog output above)")
	}
}
