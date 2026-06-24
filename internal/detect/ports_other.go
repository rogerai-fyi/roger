//go:build !linux && !darwin && !windows

package detect

// listeningPorts has no cheap, dependency-free enumeration on this platform, so it
// reports none. Detection still works via the default-endpoint, env-var, and
// explicit-upstream sources; a model on a custom port can be reached with
// `rogerai share --upstream <url>` or the guided fallback's paste-a-URL path.
func listeningPorts() []int { return nil }
