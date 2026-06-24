//go:build !(linux && amd64)

package agent

// No TEE quote generation on this platform (only linux/amd64 SEV-SNP is wired). The
// node honestly reports "no TEE" and never claims confidential here.

import "fmt"

func teeAvailable() teeKind { return "" }

func generateQuote(reportData [64]byte) ([]byte, error) {
	return nil, fmt.Errorf("TEE quote generation is not supported on this platform")
}
