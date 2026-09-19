package frame

const (
	// CodecVersion is the compact frame format implemented by this package.
	CodecVersion uint16 = 1
	// MaxLevels is the number of price levels that fit in each side's word.
	MaxLevels = 5
	// MaxTick is the largest price tick, offset, or lot quantity in codec v1.
	MaxTick uint32 = 0xffffff
)
