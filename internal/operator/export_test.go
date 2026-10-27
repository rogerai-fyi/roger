package operator

// Test-only exports for the external operator_test package (guest_routing_bdd_test.go), which
// cannot be in package operator because it imports internal/client, which imports this package.
const GoldenOpencode = goldenOpencode
