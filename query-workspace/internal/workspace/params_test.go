package workspace

import (
	"math"
	"testing"
)

func TestInlineParametersReplacesOnlyKnownTokensOutsideStringsAndComments(t *testing.T) {
	query := "MATCH (n {name: $name}) WHERE n.note <> '$name' // $name\nRETURN $unknown, $name, `$name` /* $name */"
	segments, used, err := inlineParameters(query, map[string]any{"name": `a"b`, "unused": 1})
	if err != nil {
		t.Fatal(err)
	}
	want := "MATCH (n {name: \"a\\\"b\"}) WHERE n.note <> '$name' // $name\nRETURN $unknown, \"a\\\"b\", `$name` /* $name */"
	if got := joinSegments(segments); got != want {
		t.Fatalf("inlined = %q, want %q", got, want)
	}
	marks := 0
	for _, segment := range segments {
		if segment.Param {
			marks++
			if segment.Title != `$name = "a\"b" (passed as a parameter)` {
				t.Errorf("title = %q", segment.Title)
			}
		}
	}
	if used != 1 || marks != 2 {
		t.Fatalf("used = %d, marks = %d; want 1 parameter shown twice", used, marks)
	}
}

func TestCypherLiteral(t *testing.T) {
	for _, test := range []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{true, "true"},
		{int64(9223372036854775807), "9223372036854775807"},
		{float64(1), "1.0"},
		{0.25, "0.25"},
		{"line\nbreak\\\x01", `"line\nbreak\\\u0001"`},
		{[]any{"PHP", int64(2)}, `["PHP", 2]`},
		{map[string]any{"b": false, "a`b": nil}, "{`a``b`: null, `b`: false}"},
	} {
		got, err := cypherLiteral(test.value)
		if err != nil || got != test.want {
			t.Errorf("cypherLiteral(%#v) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}
	if _, err := cypherLiteral(math.NaN()); err == nil {
		t.Error("NaN has no Cypher literal")
	}
}
