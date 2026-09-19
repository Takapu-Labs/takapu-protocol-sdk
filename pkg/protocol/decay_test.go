package protocol

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestDecayBoundariesAndRounding(t *testing.T) {
	cfg := GlobalDecayConfig{10, 13, 20, 100_000}
	for _, test := range []struct {
		age  uint64
		want uint32
	}{{0, 0}, {10, 0}, {11, 33333}, {12, 66666}, {13, 100000}, {19, 100000}} {
		got, err := DecayPPM(100, 100+test.age, cfg)
		if err != nil || got != test.want {
			t.Fatalf("age %d: %d, %v; want %d", test.age, got, err, test.want)
		}
	}
	for _, test := range []struct {
		at   uint64
		want error
	}{{99, ErrFutureFrame}, {120, ErrExpiredFrame}, {math.MaxUint64, ErrExpiredFrame}} {
		if _, err := DecayPPM(100, test.at, cfg); !errors.Is(err, test.want) {
			t.Fatalf("at %d: %v", test.at, err)
		}
	}
	cfg.MaxDecayPpm = 0
	if _, err := DecayPPM(100, 120, cfg); !errors.Is(err, ErrExpiredFrame) {
		t.Fatal(err)
	}
	// Multiplication exceeds uint32; timestamp is also beyond uint32.
	cfg = GlobalDecayConfig{1, math.MaxUint32 - 1, math.MaxUint32, 999999}
	got, err := DecayPPM(math.MaxUint32, uint64(math.MaxUint32)+uint64(math.MaxUint32)-2, cfg)
	if err != nil || got != 999998 {
		t.Fatalf("large interpolation %d %v", got, err)
	}
}

func TestDecayRejectsInvalidConfigBeforeTimestamp(t *testing.T) {
	for _, cfg := range []GlobalDecayConfig{{}, {10, 10, 20, 1}, {11, 10, 20, 1}, {0, 10, 10, 1}, {0, 10, 20, 1000000}} {
		if _, err := DecayPPM(100, 0, cfg); !errors.Is(err, ErrInvalidDecayConfig) {
			t.Fatalf("%+v: %v", cfg, err)
		}
	}
}

func TestApplyDecayIntegerRoundingAndOwnership(t *testing.T) {
	gross := big.NewInt(11)
	fee, net, err := ApplyDecay(gross, 100000)
	if err != nil || fee.Int64() != 1 || net.Int64() != 10 {
		t.Fatalf("%v %v %v", fee, net, err)
	}
	fee.SetInt64(0)
	net.SetInt64(0)
	if gross.Int64() != 11 {
		t.Fatal("modified input")
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	fee, net, err = ApplyDecay(max, 999999)
	if err != nil || new(big.Int).Add(fee, net).Cmp(max) != 0 {
		t.Fatalf("uint256 maximum: %v", err)
	}
	for _, bad := range []*big.Int{nil, big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 256)} {
		if _, _, err := ApplyDecay(bad, 0); err == nil {
			t.Fatal("accepted invalid amount")
		}
	}
	if _, _, err := ApplyDecay(big.NewInt(1), 1000000); err == nil {
		t.Fatal("accepted 100% decay")
	}
	fee, net, err = ApplyDecay(big.NewInt(0), 0)
	if err != nil || fee.Sign() != 0 || net.Sign() != 0 || fee == net {
		t.Fatal("invalid zero result")
	}
}
