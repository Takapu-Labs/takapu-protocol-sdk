// Package stream owns the shared Market Streaming implementation. Public entry
// points live in pkg/maker and pkg/router; callers use those packages.
//
// The shared client owns connection lifetime, credentials and event delivery.
// Its mutex also protects Router subscriptions, frame delivery, generations and revisions;
// methods ending in Locked require that mutex. Public wrappers add no state or
// goroutines and delegate operations directly to this implementation.
package stream
