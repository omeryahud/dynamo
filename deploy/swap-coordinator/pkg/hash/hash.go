package hash

import (
	"encoding/binary"
	"math/bits"
)

const instanceIDMask = uint64(0x001F_FFFF_FFFF_FFFF)

// HashPodName hashes a pod name to produce a consistent instance ID.
// This replicates the Rust hash_pod_name() function which uses
// DefaultHasher (SipHash-1-3 with keys 0,0) and the str Hash impl
// (bytes followed by 0xff byte).
func HashPodName(podName string) uint64 {
	data := append([]byte(podName), 0xff)
	return sipHash13(data) & instanceIDMask
}

// sipHash13 computes SipHash-1-3 with key (0, 0).
func sipHash13(msg []byte) uint64 {
	// Initialize state with key (0, 0)
	v0 := uint64(0x736f6d6570736575) // "somepseu"
	v1 := uint64(0x646f72616e646f6d) // "dorandom"
	v2 := uint64(0x6c7967656e657261) // "lygenera"
	v3 := uint64(0x7465646279746573) // "tedbytes"

	length := len(msg)
	blocks := length / 8

	// Process full 8-byte blocks
	for i := 0; i < blocks; i++ {
		m := binary.LittleEndian.Uint64(msg[i*8:])
		v3 ^= m
		// 1 compression round
		sipRound(&v0, &v1, &v2, &v3)
		v0 ^= m
	}

	// Process remaining bytes with length encoding
	var last uint64
	remaining := length & 7
	tail := msg[blocks*8:]
	for i := 0; i < remaining; i++ {
		last |= uint64(tail[i]) << (uint(i) * 8)
	}
	last |= uint64(length&0xff) << 56

	v3 ^= last
	// 1 compression round
	sipRound(&v0, &v1, &v2, &v3)
	v0 ^= last

	// Finalization: 3 rounds
	v2 ^= 0xff
	sipRound(&v0, &v1, &v2, &v3)
	sipRound(&v0, &v1, &v2, &v3)
	sipRound(&v0, &v1, &v2, &v3)

	return v0 ^ v1 ^ v2 ^ v3
}

func sipRound(v0, v1, v2, v3 *uint64) {
	*v0 += *v1
	*v1 = bits.RotateLeft64(*v1, 13)
	*v1 ^= *v0
	*v0 = bits.RotateLeft64(*v0, 32)
	*v2 += *v3
	*v3 = bits.RotateLeft64(*v3, 16)
	*v3 ^= *v2
	*v0 += *v3
	*v3 = bits.RotateLeft64(*v3, 21)
	*v3 ^= *v0
	*v2 += *v1
	*v1 = bits.RotateLeft64(*v1, 17)
	*v1 ^= *v2
	*v2 = bits.RotateLeft64(*v2, 32)
}
