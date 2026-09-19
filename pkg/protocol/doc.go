// Package protocol encodes Takapu contract calls and provides explicit, chain-bound
// reads, simulations and transactions. It never owns an RPC connection or private
// key. Amounts are token native units; simulation outputs and Swap events are net
// of output fees and decay. Callers coordinate account nonces and receipt finality.
package protocol
