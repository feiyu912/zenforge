package workspace

import "testing"

func TestPlatformFileTypeClassification(t *testing.T) {
	for _, path := range []string{"archive.ZIP", "report.pdf", "lib.a", "image.ico"} {
		if !IsBinaryPath(path) {
			t.Fatalf("IsBinaryPath(%q) = false", path)
		}
	}
	if IsBinaryPath("README.md") {
		t.Fatal("README.md classified as binary")
	}
	for _, path := range []string{"/dev/null", "/dev/urandom", "/dev/zero"} {
		if !IsBlockedDevicePath(path) {
			t.Fatalf("IsBlockedDevicePath(%q) = false", path)
		}
	}
	if IsBlockedDevicePath("dev/null") {
		t.Fatal("workspace-relative path classified as host device")
	}
}
