package providers

import "testing"

func TestReplacementNameIsNewAndDoesNotStack(t *testing.T) {
	first := replacementName("ms-fsn1-38df")
	if first == "ms-fsn1-38df" || len(first) != len("ms-fsn1-38df")+6 {
		t.Fatalf("got %q", first)
	}
	second := replacementName(first)
	if len(second) != len(first) || second[:12] != "ms-fsn1-38df" {
		t.Fatalf("suffix stacked: %q", second)
	}
	if got := sanitizeLabel(second); got != second {
		t.Fatalf("not a valid Linode label: %q", got)
	}
}
