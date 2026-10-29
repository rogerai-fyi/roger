package localplane

// Contract: features/tower/standalone_consumer_plane.feature
//
// The exposure posture: the consumer plane defaults to loopback and refuses to masquerade on
// a public or all-interfaces address without an explicit acknowledged override. An unbound
// standalone Tower on a routable address is a broker lookalike for phishing ROGER_BROKER, and
// admission does not stop that - a bind refusal does.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultBindIsLoopback(t *testing.T) {
	require.True(t, strings.HasPrefix(DefaultBind, "127.0.0.1:"), "the default must be loopback")
}
