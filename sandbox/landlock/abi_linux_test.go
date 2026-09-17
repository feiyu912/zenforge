//go:build linux

package landlock

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestAccessBitsMatchTheKernelABI checks the hand-written access bits
// against the values golang.org/x/sys/unix compiles for Linux.
//
// These bits are not cosmetic: they decide what a sandboxed process may do.
// A bit in the wrong position silently grants a different right -- a read
// bit that is really write, or a "make file" bit that lands on "remove
// file" -- and because the ruleset still installs successfully, nothing
// fails loudly. The ABI column matters too: applying a right the running
// kernel does not know makes landlock_restrict_self fail, so the minimum
// ABI recorded per bit has to match the kernel's introduction order.
func TestAccessBitsMatchTheKernelABI(t *testing.T) {
	cases := []struct {
		name     string
		got      Access
		expected uint64
	}{
		{"AccessExecute", AccessExecute, unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"AccessWriteFile", AccessWriteFile, unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
		{"AccessReadFile", AccessReadFile, unix.LANDLOCK_ACCESS_FS_READ_FILE},
		{"AccessReadDir", AccessReadDir, unix.LANDLOCK_ACCESS_FS_READ_DIR},
		{"AccessRemoveDir", AccessRemoveDir, unix.LANDLOCK_ACCESS_FS_REMOVE_DIR},
		{"AccessRemoveFile", AccessRemoveFile, unix.LANDLOCK_ACCESS_FS_REMOVE_FILE},
		{"AccessMakeChar", AccessMakeChar, unix.LANDLOCK_ACCESS_FS_MAKE_CHAR},
		{"AccessMakeDir", AccessMakeDir, unix.LANDLOCK_ACCESS_FS_MAKE_DIR},
		{"AccessMakeReg", AccessMakeReg, unix.LANDLOCK_ACCESS_FS_MAKE_REG},
		{"AccessMakeSock", AccessMakeSock, unix.LANDLOCK_ACCESS_FS_MAKE_SOCK},
		{"AccessMakeFifo", AccessMakeFifo, unix.LANDLOCK_ACCESS_FS_MAKE_FIFO},
		{"AccessMakeBlock", AccessMakeBlock, unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK},
		{"AccessMakeSym", AccessMakeSym, unix.LANDLOCK_ACCESS_FS_MAKE_SYM},
		{"AccessRefer", AccessRefer, unix.LANDLOCK_ACCESS_FS_REFER},
		{"AccessTruncate", AccessTruncate, unix.LANDLOCK_ACCESS_FS_TRUNCATE},
		{"AccessIoctlDev", AccessIoctlDev, unix.LANDLOCK_ACCESS_FS_IOCTL_DEV},
	}
	for _, testCase := range cases {
		if uint64(testCase.got) != testCase.expected {
			t.Fatalf("%s = %#x, but the kernel ABI says %#x", testCase.name, uint64(testCase.got), testCase.expected)
		}
	}
	// Every right has a minimum ABI: a right without one would be applied on
	// a kernel that does not know it, which makes the ruleset fail to
	// install rather than fail to restrict.
	for _, right := range accessByABI {
		if right.abi <= 0 {
			t.Fatalf("%#x has no minimum ABI", right.access)
		}
		if right.abi > MaxSupportedABI {
			t.Fatalf("%#x needs ABI %d, above MaxSupportedABI %d", right.access, right.abi, MaxSupportedABI)
		}
	}
}

// TestAccessBitsAreDistinctAndOrdered catches a copy-paste bit (two rights
// sharing a position, so one of them is silently not granted) and a right
// pinned to the wrong ABI generation.
func TestAccessBitsAreDistinctAndOrdered(t *testing.T) {
	seen := map[Access]bool{}
	for _, right := range accessByABI {
		if seen[right.access] {
			t.Fatalf("%#x appears twice in the rights table", right.access)
		}
		seen[right.access] = true
	}
	// Refer arrived in ABI 2, Truncate in 3, IOCTL_DEV in 5: reading them
	// out of the table catches a transposed column. Everything else is
	// ABI 1, and the table has to cover all 16 rights.
	if len(accessByABI) != 16 {
		t.Fatalf("the rights table has %d entries, want 16", len(accessByABI))
	}
	byAccess := map[Access]int{}
	for _, right := range accessByABI {
		byAccess[right.access] = right.abi
	}
	for _, testCase := range []struct {
		right Access
		abi   int
	}{
		{AccessExecute, 1}, {AccessRefer, 2}, {AccessTruncate, 3}, {AccessIoctlDev, 5},
	} {
		abi, ok := byAccess[testCase.right]
		if !ok {
			t.Fatalf("%#x is missing from the rights table", testCase.right)
		}
		if abi != testCase.abi {
			t.Fatalf("%#x has minimum ABI %d, want %d", testCase.right, abi, testCase.abi)
		}
	}
}
