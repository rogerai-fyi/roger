package localplane

// DefaultBind is where the consumer plane listens when the operator names no address:
// loopback, reachable only from the Tower host itself. Widening to the LAN is a deliberate,
// explicit choice, never the default. The posture check itself is internal/netbind.Resolve.
const DefaultBind = "127.0.0.1:8787"
