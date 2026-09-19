package maker

import (
	stream "github.com/Takapu-Labs/takapu-protocol-sdk/internal"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/common"
)

// PairParams contains the on-chain pair's token addresses, decimals, tick size
// and lot size. Callers must supply current, trusted metadata.
type PairParams = frame.PairParams

// Signer signs EIP-712 digests and owns private-key storage. Its address must be
// the maker's authorized frame signer; Maker.Publish and
// Maker.SubmitFrameUpdate verify recovery locally.
type Signer = frame.Signer

// PriceLevel is a price in quote tokens per base token and an amount in base
// tokens. Both must be positive plain decimal strings of at most 64 characters.
// Prices round down for bids and up for asks to the pair's price tick size;
// amounts round down to whole lots. Conversion uses exact decimal arithmetic,
// without floats. The resulting tick and lot counts must be positive uint24s.
// Rounding never raises a bid, lowers an ask, or increases an amount; already
// aligned values are unchanged.
type PriceLevel struct {
	Price string `json:"price"`
	// Amount is this level's raw depth in base tokens, converted to protocol
	// QtyLots without adjusting for on-chain fills. It is not cumulative across
	// levels. Within the same maker lifecycle and major version, each side's
	// filled-lot counter skips a best-first prefix of the new book, so raw depth
	// can exceed remaining executable depth. For example, a single 10-lot level
	// with 4 lots already filled has 6 lots remaining if a minor update keeps
	// the raw depth at 10 lots; supplying 6 lots leaves only 2 lots executable,
	// assuming no further fills before the update is applied on-chain.
	Amount string `json:"amount"`
}

// PublishParams describes a quote and signing context for Maker.Publish or
// Maker.SubmitFrameUpdate. Each side supports up to five levels: bids
// strictly descending, asks strictly ascending, with best bid below best ask.
// Locked or crossed input books are rejected even if rounding would uncross them.
// Prices must remain strictly ordered after rounding; levels that round to the
// same tick or zero lots are rejected, never merged or dropped.
// One-sided books are valid; both empty withdraw the quote. Versions and
// UpdatedAt (Unix seconds) are supplied unchanged; the caller manages inventory
// and reserves versions before publishing, including across restarts and failed
// submissions. When applied on-chain within a maker
// lifecycle, increasing minor within the same major preserves each side's
// filled-lot counter; increasing major resets both counters. Callers must account
// for these counters when determining remaining executable inventory from the
// raw PriceLevel amounts.
type PublishParams struct {
	ChainID                 uint64
	Protocol, Maker         common.Address // Protocol proxy and maker contract.
	Pair                    PairParams
	Bids, Asks              []PriceLevel
	UpdatedAt, MajorVersion uint32
	MinorVersion            uint8
	Signer                  Signer
}

// ClientPhase identifies the current connection lifecycle state.
type ClientPhase = stream.ClientPhase

// ClientStatus is a snapshot of the connection state and generation.
type ClientStatus = stream.ClientStatus

// ClientStats combines lifetime counters with the current event queue size.
type ClientStats = stream.ClientStats

// EventKind identifies which event payload is populated.
type EventKind = stream.EventKind

// MakerEvent reports a connection change or an asynchronous frame rejection.
type MakerEvent = stream.MakerEvent
