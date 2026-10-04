package bloom

import "hash/fnv"

// BitsPerKey of 10 gives ~1% false positives.
const BitsPerKey = 10

// Build returns a filter holding every key.
func Build(keys [][]byte, bitsPerKey int) []byte {
	// k = bitsPerKey · ln 2 minimizes false positives.
	k := min(max(int(float64(bitsPerKey)*0.69), 1), 30)

	// A tiny set still gets 64 bits, or a handful of keys fills the array.
	bits := max(len(keys)*bitsPerKey, 64)
	bytes := (bits + 7) / 8
	bits = bytes * 8

	filter := make([]byte, bytes+1)
	filter[bytes] = byte(k)
	for _, key := range keys {
		h := hash(key)
		delta := h>>17 | h<<15 // rotate right 17 bits
		for range k {
			pos := h % uint32(bits)
			filter[pos/8] |= 1 << (pos % 8)
			h += delta
		}
	}
	return filter
}

// MayContain reports false only if key was definitely never added.
func MayContain(filter, key []byte) bool {
	if len(filter) < 2 {
		return true // no usable filter: can't rule anything out
	}
	bytes := len(filter) - 1
	bits := uint32(bytes * 8)
	k := int(filter[bytes])
	if k < 1 || k > 30 {
		return true // a k we never write: treat as unknown, not as absent
	}
	h := hash(key)
	delta := h>>17 | h<<15
	for range k {
		pos := h % bits
		if filter[pos/8]&(1<<(pos%8)) == 0 {
			return false
		}
		h += delta
	}
	return true
}

func hash(key []byte) uint32 {
	h := fnv.New32a()
	_, _ = h.Write(key)
	return h.Sum32()
}
