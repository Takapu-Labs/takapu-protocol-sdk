package frame

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// SignedFrame carries a compact quote, maker and EIP-712 signature.
// Constructing it does not verify signer identity; use VerifySignature for that.
// Signature contains r||s||v and may be modified by the caller. Signing helpers
// produce v=27/28; NormalizeSignature also accepts and converts v=0/1.
type SignedFrame struct {
	Frame     CompactFrame
	Maker     common.Address
	Signature []byte
}

// SigningDomain identifies the chain and Protocol proxy for EIP-712.
// Protocol must be the proxy address, not an implementation address.
type SigningDomain struct {
	ChainID  uint64
	Protocol common.Address
}

// Signer signs a raw EIP-712 digest without a personal_sign prefix. The signer
// owns private-key storage and must honor context cancellation. Address returns
// the expected recovery address; SignDigest returns a 65-byte low-s signature
// with v=0/1 or v=27/28.
type Signer interface {
	Address() common.Address
	SignDigest(ctx context.Context, digest common.Hash) ([]byte, error)
}

var (
	domainTypeHash    = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	frameTypeHash     = crypto.Keccak256Hash([]byte("UpdateFrame(bytes32 header,bytes32 bidLevels,bytes32 askLevels,address maker)"))
	domainNameHash    = crypto.Keccak256Hash([]byte("Takapu Protocol"))
	domainVersionHash = crypto.Keccak256Hash([]byte("1"))
)

func validateDomain(d SigningDomain) error {
	if d.ChainID == 0 {
		return errors.New("frame: chain ID must be positive")
	}
	if d.Protocol == (common.Address{}) {
		return errors.New("frame: protocol must be a nonzero proxy address")
	}
	return nil
}

func digest(d SigningDomain, maker common.Address, f CompactFrame) common.Hash {
	separator := crypto.Keccak256(
		domainTypeHash[:],
		domainNameHash[:],
		domainVersionHash[:],
		common.LeftPadBytes(new(big.Int).SetUint64(d.ChainID).Bytes(), 32),
		common.LeftPadBytes(d.Protocol[:], 32),
	)
	payload := crypto.Keccak256(
		frameTypeHash[:],
		f.Header[:],
		f.BidLevels[:],
		f.AskLevels[:],
		common.LeftPadBytes(maker[:], 32),
	)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, separator, payload)
}

// SigningDigest validates a compact frame and returns its maker-bound
// EIP-712 digest. No personal-message prefix is applied.
func SigningDigest(domain SigningDomain, maker common.Address, value CompactFrame) (common.Hash, error) {
	if err := validateDomain(domain); err != nil {
		return common.Hash{}, err
	}
	if maker == (common.Address{}) {
		return common.Hash{}, errors.New("frame: maker is zero")
	}
	if err := ValidateCompactFrame(value); err != nil {
		return common.Hash{}, err
	}
	return digest(domain, maker, value), nil
}

// NormalizeSignature copies a canonical low-s 65-byte r||s||v signature and
// normalizes v=0/1 or 27/28 to 27/28. It does not establish signer identity.
func NormalizeSignature(sig []byte) ([]byte, error) {
	if len(sig) != 65 {
		return nil, errors.New("frame: signature must contain exactly 65 bytes")
	}
	out := append([]byte(nil), sig...)
	switch out[64] {
	case 27, 28:
		out[64] -= 27
	case 0, 1:
	default:
		return nil, errors.New("frame: signature recovery ID must be 0/1 or 27/28")
	}
	r := new(big.Int).SetBytes(out[:32])
	s := new(big.Int).SetBytes(out[32:64])
	if !crypto.ValidateSignatureValues(out[64], r, s, true) {
		return nil, errors.New("frame: signature r/s are out of range or s is not low")
	}
	out[64] += 27
	return out, nil
}

// VerifySignature verifies a canonical quote and its offline signer identity.
// It does not establish that expectedSigner is currently authorized on-chain.
func VerifySignature(domain SigningDomain, value SignedFrame, expectedSigner common.Address) error {
	if expectedSigner == (common.Address{}) {
		return errors.New("frame: expected signer is zero")
	}
	d, err := SigningDigest(domain, value.Maker, value.Frame)
	if err != nil {
		return err
	}
	sig, err := NormalizeSignature(value.Signature)
	if err != nil {
		return err
	}
	sig[64] -= 27
	pub, err := crypto.SigToPub(d[:], sig)
	if err != nil {
		return fmt.Errorf("frame: recover signer: %w", err)
	}
	if crypto.PubkeyToAddress(*pub) != expectedSigner {
		return errors.New("frame: recovered signer does not match expected signer")
	}
	return nil
}

// SignFrame signs a validated calldata frame, validates the recovered signer,
// and returns a signature with v=27/28. Storage frames with filled counters or
// writtenAt set are rejected. The signer owns any private-key handling.
func SignFrame(ctx context.Context, domain SigningDomain, maker common.Address, value CompactFrame, signer Signer) (SignedFrame, error) {
	if ctx == nil {
		return SignedFrame{}, errors.New("frame: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return SignedFrame{}, err
	}
	if signer == nil {
		return SignedFrame{}, errors.New("frame: signer is nil")
	}
	rv := reflect.ValueOf(signer)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if rv.IsNil() {
			return SignedFrame{}, errors.New("frame: signer is nil")
		}
	}
	expected := signer.Address()
	if expected == (common.Address{}) {
		return SignedFrame{}, errors.New("frame: signer address is zero")
	}
	d, err := SigningDigest(domain, maker, value)
	if err != nil {
		return SignedFrame{}, err
	}
	sig, err := signer.SignDigest(ctx, d)
	if err != nil {
		return SignedFrame{}, fmt.Errorf("frame: sign digest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return SignedFrame{}, err
	}
	normalized, err := NormalizeSignature(sig)
	if err != nil {
		return SignedFrame{}, err
	}
	out := SignedFrame{Frame: value, Maker: maker, Signature: normalized}
	if err := VerifySignature(domain, out, expected); err != nil {
		return SignedFrame{}, err
	}
	return out, nil
}
