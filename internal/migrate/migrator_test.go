package migrate

import (
	"reflect"
	"testing"
)

func TestParseSearchPath(t *testing.T) {
	t.Parallel()

	got := parseSearchPath(`context, public, "$user", "tenant_default"`)
	want := []string{"context", "public", "$user", "tenant_default"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSearchPath() = %#v, want %#v", got, want)
	}
}

func TestShouldEnsureSchema(t *testing.T) {
	t.Parallel()

	cases := []struct {
		schema string
		want   bool
	}{
		{schema: "", want: false},
		{schema: "$user", want: false},
		{schema: "public", want: false},
		{schema: "pg_catalog", want: false},
		{schema: "context", want: true},
		{schema: "tenant_default", want: true},
	}

	for _, tc := range cases {
		if got := shouldEnsureSchema(tc.schema); got != tc.want {
			t.Fatalf("shouldEnsureSchema(%q) = %v, want %v", tc.schema, got, tc.want)
		}
	}
}

func TestQuoteIdentifier(t *testing.T) {
	t.Parallel()

	got := quoteIdentifier(`tenant"default`)
	want := `"tenant""default"`
	if got != want {
		t.Fatalf("quoteIdentifier() = %q, want %q", got, want)
	}
}
