//go:build linux && amd64

package agent

// AMD SEV-SNP quote generation via the guest /dev/sev-guest device, using
// github.com/google/go-sev-guest. We do NOT hand-roll any crypto: the device + the
// AMD firmware produce a VCEK-signed ATTESTATION_REPORT, and GetRawExtendedReport
// returns the report together with its certificate table (VCEK chain) so the broker
// can verify VCEK -> ASK -> ARK to the AMD root. The returned bytes are exactly the
// wire format the broker parses with abi.ReportCertsToProto.

import (
	"fmt"

	"github.com/google/go-sev-guest/client"
)

// teeAvailable returns teeSEVSNP only if the SEV-SNP guest device opens (i.e. we are
// actually inside an SEV-SNP confidential VM). On a normal machine OpenDevice fails
// and we honestly report "no TEE".
func teeAvailable() teeKind {
	d, err := client.OpenDevice()
	if err != nil {
		return ""
	}
	_ = d.Close()
	return teeSEVSNP
}

// generateQuote produces the raw extended SEV-SNP report (ATTESTATION_REPORT || VCEK
// cert table) with the given report_data. The cert table lets the broker build the
// VCEK chain without a second round-trip; if the device omits it, the broker fills it
// from the AMD KDS.
func generateQuote(reportData [64]byte) ([]byte, error) {
	d, err := client.OpenDevice()
	if err != nil {
		return nil, fmt.Errorf("open /dev/sev-guest: %w", err)
	}
	defer d.Close()
	report, certs, err := client.GetRawExtendedReport(d, reportData)
	if err != nil {
		return nil, fmt.Errorf("get extended report: %w", err)
	}
	return append(report, certs...), nil
}
