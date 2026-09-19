package main

import (
	"context"
	"errors"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// prepareNextQuote applies the same timestamp and durable version policy to
// WebSocket publishing and direct on-chain submission.
func prepareNextQuote(ctx context.Context, rpc *ethclient.Client, contract *protocol.Client, state chainState, signer localSigner, cfg *config, configPath string, i, count int) (maker.PublishParams, error) {
	head, err := rpc.HeaderByNumber(ctx, nil)
	if err != nil {
		return maker.PublishParams{}, err
	}
	if head == nil || head.Number == nil || head.Number.Sign() < 0 {
		return maker.PublishParams{}, errors.New("RPC returned a block without a valid number")
	}
	now, updated := uint64(time.Now().Unix()), head.Time
	if now < updated {
		updated = now
	}
	if updated <= state.Cutoff || updated > uint64(^uint32(0)) {
		return maker.PublishParams{}, errors.New("quote timestamp outside active maker lifecycle or uint32 range")
	}
	decay, err := contract.GlobalDecayConfig(ctx, head.Number)
	if err != nil {
		return maker.PublishParams{}, err
	}
	if _, err := protocol.DecayPPM(uint32(updated), now, decay); err != nil {
		return maker.PublishParams{}, err
	}
	major, minor := cfg.NextVersion.Major, cfg.NextVersion.Minor
	cfg.logf("quote", "%d/%d: version=(%d,%d), block=%s, quote time=%s, age=%ds, maximum valid age=%ds", i+1, count, major, minor, head.Number, time.Unix(int64(updated), 0).Format(time.RFC3339), now-updated, decay.MaxAge)
	params := maker.PublishParams{
		ChainID:      cfg.ChainID,
		Protocol:     common.HexToAddress(cfg.Protocol),
		Maker:        common.HexToAddress(cfg.Maker),
		Pair:         state.Pair,
		Bids:         cfg.Quote.Bids,
		Asks:         cfg.Quote.Asks,
		UpdatedAt:    uint32(updated),
		MajorVersion: major,
		MinorVersion: uint8(minor),
		Signer:       signer,
	}
	for index, bid := range params.Bids {
		cfg.logf("bid", "Level %d: price=%s quote/base, quantity=%s base", index+1, bid.Price, bid.Amount)
	}
	for index, ask := range params.Asks {
		cfg.logf("ask", "Level %d: price=%s quote/base, quantity=%s base", index+1, ask.Price, ask.Amount)
	}
	// Reserve before signing or submission, including NoSend and failed attempts.
	if err := cfg.reserveVersion(configPath); err != nil {
		return maker.PublishParams{}, err
	}
	cfg.logf("version", "Next version (%d,%d) saved; this quote version remains reserved even if signing or submission fails", cfg.NextVersion.Major, cfg.NextVersion.Minor)
	return params, nil
}
