package listing

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestListingRawSizesProduceExactReadableFrame(t *testing.T) {
	// The testnet's 8/18-decimal fixture: a raw tick of 10^26 and lot of 10000.
	// Exercise the public HTTP decoder as well as the frame conversion.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, detailEnvelope())
	}))
	defer srv.Close()
	pair, err := testClient(t, srv, nil).ListPair(context.Background(), testChainID, testPairID)
	if err != nil {
		t.Fatal(err)
	}
	params, err := pair.FrameParams()
	if err != nil {
		t.Fatal(err)
	}
	if params.PriceTickSize.String() != "100000000000000000000000000" || params.LotSize.String() != "10000" {
		t.Fatalf("raw sizes were rescaled: %+v", params)
	}
	prepared, err := frame.Prepare(frame.FrameSpec{
		ChainID: testChainID, Protocol: common.HexToAddress("0x3333333333333333333333333333333333333333"),
		Maker: common.HexToAddress("0x4444444444444444444444444444444444444444"), Pair: params,
		BaseTick: 1, Bids: []frame.Level{{QtyLots: 1}}, Asks: []frame.Level{{Offset: 1, QtyLots: 2}},
		UpdatedAt: 1, MajorVersion: 1, CodecVersion: frame.CodecVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := prepared.SigningPayload()
	signature, err := crypto.Sign(payload.Digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := prepared.AttachSignature(signature, crypto.PubkeyToAddress(key.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if wire.Bids[0].Price != "0.01" || wire.Bids[0].Amount != "0.0001" ||
		wire.Asks[0].Price != "0.02" || wire.Asks[0].Amount != "0.0002" {
		t.Fatalf("incorrect readable sizes: bids=%v asks=%v", wire.Bids, wire.Asks)
	}
	params.PriceTickSize.SetInt64(1)
	params.LotSize.SetInt64(1)
	fresh, err := pair.FrameParams()
	if err != nil || fresh.PriceTickSize.String() != pair.PriceTickSize || fresh.LotSize.String() != pair.LotSize {
		t.Fatalf("returned integers share mutable state: %+v, %v", fresh, err)
	}
}

func TestFrameParamsRejectInvalidManualPair(t *testing.T) {
	data, _ := json.Marshal(pairObject())
	pair, err := decodePair(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Pair)
	}{
		{"readable tick", func(p *Pair) { p.PriceTickSize = "0.01" }},
		{"readable lot", func(p *Pair) { p.LotSize = "0.0001" }},
		{"tick overflow", func(p *Pair) { p.PriceTickSize = new(big.Int).Lsh(big.NewInt(1), 128).String() }},
		{"zero lot", func(p *Pair) { p.LotSize = "0" }},
		{"unregistered", func(p *Pair) { p.PairID = 0 }},
		{"invalid tokens", func(p *Pair) { p.QuoteToken = p.BaseToken }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := pair
			tc.change(&invalid)
			if _, err := invalid.FrameParams(); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("invalid pair accepted: %v", err)
			}
		})
	}
}
