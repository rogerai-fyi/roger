package edge

import "rogerai.fm/roger/v7/internal/edge/wire"

// The mDNS codec, the multicast transport and the advertisement live in internal/edge/wire, a
// leaf package that imports nothing from the Edge, so a Core-free binary (roger-tower-local)
// can advertise through the same codec. These aliases keep every Edge caller unchanged.
type (
	Advert       = wire.Advert
	Transport    = wire.Transport
	Responder    = wire.Responder
	BrowseResult = wire.BrowseResult
)

const DefaultService = wire.DefaultService

var (
	ErrNoLAN                       = wire.ErrNoLAN
	AdvertiseAddrs                 = wire.AdvertiseAddrs
	HasLAN                         = wire.HasLAN
	InterfaceAddrs                 = wire.InterfaceAddrs
	NewMulticastTransport          = wire.NewMulticastTransport
	NewRejoiningMulticastTransport = wire.NewRejoiningMulticastTransport
	NewResponder                   = wire.NewResponder
	NewLiveResponder               = wire.NewLiveResponder
	Browse                         = wire.Browse
)
