package captions

import (
	"os"
	"testing"
)

// The extension ships its own copy of observer.js, since it has no build step
// that could embed this one.
func TestExtensionObserverIsCurrent(t *testing.T) {
	got, err := os.ReadFile("../../extension/observer.js")
	if err != nil {
		t.Fatalf("%v; run make extension", err)
	}
	if string(got) != ObserverJS {
		t.Fatal("extension/observer.js differs from internal/captions/observer.js; run make extension")
	}
}
