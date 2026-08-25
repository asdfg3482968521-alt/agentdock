package envstore

import (
	"runtime"
	"testing"
)

func TestMergeUsesPlatformEnvironmentKeyIdentity(t *testing.T) {
	base := map[string]string{"Parent_Casing": "parent-value"}
	merged := Merge(base, map[string]string{"PARENT_CASING": "override-value"})

	if runtime.GOOS == "windows" {
		if len(merged) != 1 {
			t.Fatalf("Windows logical key count = %d, want 1: %#v", len(merged), merged)
		}
		if merged["Parent_Casing"] != "override-value" {
			t.Fatalf("Windows override = %q, want override-value", merged["Parent_Casing"])
		}
		if _, exists := merged["PARENT_CASING"]; exists {
			t.Fatal("Windows merge replaced parent canonical casing instead of preserving it")
		}
		return
	}

	if len(merged) != 2 {
		t.Fatalf("non-Windows key count = %d, want 2: %#v", len(merged), merged)
	}
	if merged["Parent_Casing"] != "parent-value" || merged["PARENT_CASING"] != "override-value" {
		t.Fatalf("non-Windows case-sensitive merge changed semantics: %#v", merged)
	}
}
