package main

import (
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"
)

func (c config) logf(stage, format string, args ...any) {
	message := c.redact(fmt.Errorf(format, args...))
	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().Format("15:04:05.000"), stage, message)
}

// Log the endpoint's hostname, never URL credentials or query parameters.
func endpointHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "(no valid hostname configured)"
	}
	return u.Hostname()
}

// Format native token units exactly, without floating-point rounding.
func tokenUnits(raw string, decimals uint8) string {
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.Sign() < 0 {
		return raw
	}
	digits := value.String()
	if decimals == 0 {
		return digits
	}
	scale := int(decimals)
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	point := len(digits) - scale
	fraction := strings.TrimRight(digits[point:], "0")
	if fraction == "" {
		return digits[:point]
	}
	return digits[:point] + "." + fraction
}
