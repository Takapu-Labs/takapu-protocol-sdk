package listing_test

import (
	"context"
	"fmt"
	"math/big"
	"os"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Configure a trusted RPC endpoint and deployed protocol proxy for the same
// chain as Listing. This example performs read-only calls and does not sign.
func ExamplePair_FrameParams() {
	ctx := context.Background()
	client, err := listing.NewClient(listing.Config{APIKey: os.Getenv("TAKAPU_API_KEY")})
	if err != nil {
		panic(err)
	}
	rpc, err := ethclient.DialContext(ctx, os.Getenv("TAKAPU_RPC_URL"))
	if err != nil {
		panic(err)
	}
	defer rpc.Close()
	contract, err := protocol.NewClient(ctx, rpc, protocol.Config{
		ChainID: 97, Proxy: common.HexToAddress(os.Getenv("TAKAPU_PROTOCOL_PROXY")),
	}) // NewClient verifies the RPC chain ID.
	if err != nil {
		panic(err)
	}
	pair, err := client.ListPair(ctx, contract.ChainID(), 1)
	if err != nil {
		panic(err)
	}
	if !pair.Active || pair.Gray {
		panic("Listing pair is not usable")
	}
	params, err := pair.FrameParams() // Raw integers: do not multiply by token scales.
	if err != nil {
		panic(err)
	}
	// Use one fixed block for pairConfigs and both token-decimal reads.
	block, err := rpc.BlockNumber(ctx)
	if err != nil {
		panic(err)
	}
	blockNumber := new(big.Int).SetUint64(block)
	onchain, err := contract.PairConfig(ctx, pair.PairID, blockNumber)
	if err != nil {
		panic(err)
	}
	if !onchain.Active || params.BaseToken != onchain.BaseToken || params.QuoteToken != onchain.QuoteToken ||
		params.PriceTickSize.Cmp(onchain.PriceTickSize) != 0 || params.LotSize.Cmp(onchain.LotSize) != 0 {
		panic("Listing configuration differs from the selected on-chain pair")
	}
	for _, token := range []struct {
		address  common.Address
		decimals uint8
	}{
		{params.BaseToken, params.BaseDecimals},
		{params.QuoteToken, params.QuoteDecimals},
	} {
		// ERC-20 decimals() selector; the ABI response is one uint8 word.
		data, err := rpc.CallContract(ctx, ethereum.CallMsg{
			To: &token.address, Data: []byte{0x31, 0x3c, 0xe5, 0x67},
		}, blockNumber)
		if err != nil {
			panic(err)
		}
		if len(data) != 32 || new(big.Int).SetBytes(data).Cmp(new(big.Int).SetUint64(uint64(token.decimals))) != 0 {
			panic("Listing token decimals differ from ERC-20 decimals()")
		}
	}
	// Pass params unchanged to maker.PublishParams.Pair or frame.FrameSpec.Pair.
	fmt.Printf("Verified raw tick=%s raw lot=%s at block %d\n", params.PriceTickSize, params.LotSize, block)
}
