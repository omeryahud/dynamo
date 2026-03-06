package hash

import (
	"testing"
)

func TestHashPodName(t *testing.T) {
	// Reference values from Rust's hash_pod_name() using DefaultHasher (SipHash-1-3)
	tests := []struct {
		podName  string
		expected uint64
	}{
		{"worker-0", 1186445869714687},
		{"worker-99999", 7410589986301036},
		{"deployment-with-hash-suffix-a1b2c3d4e5f6", 2590930466072916},
		{"fake-name-1-0-worker-nrdfv", 1009006049675451},
	}

	for _, tt := range tests {
		t.Run(tt.podName, func(t *testing.T) {
			got := HashPodName(tt.podName)
			if got != tt.expected {
				t.Errorf("HashPodName(%q) = %d, want %d", tt.podName, got, tt.expected)
			}
		})
	}
}

func TestHashPodNameMaskBits(t *testing.T) {
	// Verify the top 11 bits are always clear
	names := []string{"a", "test-pod", "very-long-deployment-name-with-many-characters-12345"}
	for _, name := range names {
		h := HashPodName(name)
		if h>>53 != 0 {
			t.Errorf("HashPodName(%q) = %d has bits set above bit 52", name, h)
		}
	}
}
