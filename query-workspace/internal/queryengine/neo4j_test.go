package queryengine

import (
	"testing"
)

func TestRowCollectorEnforcesRowAndByteLimits(t *testing.T) {
	collector := newRowCollector([]string{"value"}, 2, 20)

	if !collector.add([]any{"first"}) {
		t.Fatal("first row should fit")
	}
	if !collector.add([]any{"second"}) {
		t.Fatal("second row should fit")
	}
	if collector.add([]any{"third"}) {
		t.Fatal("row limit should reject third row")
	}
	if !collector.truncated {
		t.Fatal("row limit should mark results truncated")
	}

	collector = newRowCollector([]string{"value"}, 10, 5)
	if collector.add([]any{"too long"}) {
		t.Fatal("byte limit should reject oversized row")
	}
	if !collector.truncated || len(collector.result().Rows) != 0 {
		t.Fatal("byte limit should mark truncated without storing oversized row")
	}
}
