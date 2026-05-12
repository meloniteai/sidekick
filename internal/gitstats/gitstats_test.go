package gitstats

import "testing"

func TestParseNumstatLineTextFile(t *testing.T) {
	got, ok := parseNumstatLine("12\t3\tinternal/foo.go")
	if !ok {
		t.Fatal("expected ok parse")
	}
	if got.Path != "internal/foo.go" || got.Added != 12 || got.Removed != 3 || got.Binary {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestParseNumstatLineBinary(t *testing.T) {
	got, ok := parseNumstatLine("-\t-\tassets/logo.png")
	if !ok {
		t.Fatal("expected ok parse for binary file")
	}
	if !got.Binary || got.Path != "assets/logo.png" {
		t.Fatalf("expected binary, got %+v", got)
	}
	if got.Added != 0 || got.Removed != 0 {
		t.Fatalf("binary should have zero counts: %+v", got)
	}
}

func TestParseNumstatLineRejectsGarbage(t *testing.T) {
	for _, line := range []string{
		"",
		"only one field",
		"two\tfields",
		"abc\tdef\tfile.go",
	} {
		if _, ok := parseNumstatLine(line); ok {
			t.Errorf("expected reject for %q", line)
		}
	}
}
