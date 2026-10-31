package noryxv1

import "testing"

func TestProcessLimit(t *testing.T) {
	if got := ProcessLimit(0); got != DefaultProcessLimit {
		t.Errorf("ProcessLimit(0) = %d", got)
	}
	if got := ProcessLimit(4096); got != 4096 {
		t.Errorf("ProcessLimit(4096) = %d", got)
	}
	checkValid(t, ValidProcessLimit, []uint32{0, 1024, 4096, 8192}, []uint32{1, 1023, 8193, 1 << 31})
}

// The initial heap is 0, for as large as the memory, or from 512 MB to the memory; Aikar's
// flags only take 0.
func TestValidInitMemory(t *testing.T) {
	for _, tc := range []struct {
		init, memory uint32
		aikar        bool
		want         bool
	}{
		{0, 2048, false, true},
		{0, 2048, true, true},
		{512, 2048, false, true},
		{1024, 2048, false, true},
		{2048, 2048, false, true},
		{511, 2048, false, false},
		{1, 2048, false, false},
		{2049, 2048, false, false},
		{1024, 2048, true, false},
		{2048, 2048, true, false},
	} {
		if got := ValidInitMemory(tc.init, tc.memory, tc.aikar); got != tc.want {
			t.Errorf("ValidInitMemory(%d, %d, %v) = %v", tc.init, tc.memory, tc.aikar, got)
		}
	}
}
