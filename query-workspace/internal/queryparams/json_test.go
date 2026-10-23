package queryparams

import (
	"testing"
)

func TestDecodeRejectsInvalidOrOutOfRangeParameters(t *testing.T) {
	for _, text := range []string{
		`null`, `[]`, `{"limit":9223372036854775808}`, `{"limit":-9223372036854775809}`,
		`{"nested":[{"id":9223372036854775808}]}`, `{"score":1e309}`,
		`{} {}`, `{"limit":10} trailing`, `{"limit":`,
	} {
		t.Run(text, func(t *testing.T) {
			_, err := Decode([]byte(text))
			if err == nil {
				t.Fatal("invalid parameter object was accepted")
			}
		})
	}
}
