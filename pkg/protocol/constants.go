package protocol

// PPMDenominator is the number of parts per million in a whole amount.
const PPMDenominator uint32 = 1_000_000

const (
	// MethodSwap spends only the input required for the available fill and fees.
	MethodSwap SwapMethod = "swap"
	// MethodSwapExactIn spends the full requested input, including any remainder.
	MethodSwapExactIn SwapMethod = "swapExactIn"
	// MethodSwapWithCallback is encoded for use by an authorized contract payer.
	MethodSwapWithCallback SwapMethod = "swapWithCallback"
)

const erc20ABIJSON = `[
  {
    "type": "function",
    "name": "allowance",
    "stateMutability": "view",
    "inputs": [
      {"name": "owner", "type": "address"},
      {"name": "spender", "type": "address"}
    ],
    "outputs": [{"name": "remaining", "type": "uint256"}]
  },
  {
    "type": "function",
    "name": "approve",
    "stateMutability": "nonpayable",
    "inputs": [
      {"name": "spender", "type": "address"},
      {"name": "amount", "type": "uint256"}
    ],
    "outputs": [{"name": "success", "type": "bool"}]
  }
]`
