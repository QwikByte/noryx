package noryxv1

import (
	"strings"
	"testing"
	"time"
)

// checkProblem checks a message of a rule: empty if want is, or else containing want.
func checkProblem(t *testing.T, name, got, want string) {
	t.Helper()
	if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
		t.Errorf("%s: %q, want %q", name, got, want)
	}
}

// checkValid checks the answers of a rule for accepted and refused values.
func checkValid[T any](t *testing.T, valid func(T) bool, accepted, refused []T) {
	t.Helper()
	for _, v := range accepted {
		if !valid(v) {
			t.Errorf("%#v refused", v)
		}
	}
	for _, v := range refused {
		if valid(v) {
			t.Errorf("%#v accepted", v)
		}
	}
}

// Time zones end up in a variable of the image, so only names of IANA time zones pass.
func TestValidTimeZone(t *testing.T) {
	checkValid(t, ValidTimeZone,
		[]string{"", "UTC", "Europe/Berlin", "America/Port-au-Prince", "America/Argentina/Buenos_Aires", "Etc/GMT+5", "Etc/GMT-14"},
		[]string{
			"Local", "Mars/Olympus", "Europe/Atlantis", "/etc/localtime", "../../etc/passwd", "Europe/../../etc/passwd",
			"Europe/Berlin ", " Europe/Berlin", "Europe/Berlin\n", "Europe/Berlin;rm -rf /", "Europe/Berlin$(id)", "Europe/Berlin\x00",
			"Europe/" + strings.Repeat("a", 64),
		})
}

func TestValidJava(t *testing.T) {
	checkValid(t, ValidJava, []string{"", "8", "11", "17", "21", "25"}, []string{"16", "7", "21 ", "latest", "java21", "21.0.4"})
}

// The heap of a server and the memory of its container convert both ways, and the heap for a
// container fits into it; rounding, it may be 1 MB less than the largest.
func TestContainerMemory(t *testing.T) {
	for heap, container := range map[uint32]int64{0: 256, 512: 896, 1024: 1536, 4096: 5376, MaxMemoryMB: 82176} {
		if got := ContainerMemoryMB(heap); got != container {
			t.Errorf("ContainerMemoryMB(%d) = %d, want %d", heap, got, container)
		}
		if got := MaxHeapMB(container); got != int64(heap) {
			t.Errorf("MaxHeapMB(%d) = %d, want %d", container, got, heap)
		}
	}
	for container := range int64(100_000) {
		heap := MaxHeapMB(container)
		if heap < 0 || heap > 0 && ContainerMemoryMB(uint32(heap)) > container || ContainerMemoryMB(uint32(heap+2)) <= container {
			t.Fatalf("MaxHeapMB(%d) = %d, which doesn't fit or is too small", container, heap)
		}
	}
}

func TestStopTimeout(t *testing.T) {
	if got := StopTimeout(0); got != DefaultStopTimeout {
		t.Errorf("StopTimeout(0) = %v", got)
	}
	if got := StopTimeout(45); got != 45*time.Second {
		t.Errorf("StopTimeout(45) = %v", got)
	}
	checkValid(t, ValidStopTimeout, []uint32{0, 30, 60, 600}, []uint32{1, 29, 601, 1 << 31})
}
