package extent

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	got := Normalize([]Extent{{100, 10}, {0, 10}, {5, 10}, {200, 0}, {112, 3}}, 2)
	want := []Extent{{0, 15}, {100, 15}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestComplement(t *testing.T) {
	got := Complement([]Extent{{10, 10}, {30, 5}}, 0, 50)
	want := []Extent{{0, 10}, {20, 10}, {35, 15}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFromBitmap(t *testing.T) {
	bm := []byte{0b00000110, 0xFF, 0x00, 0b10000000}
	got := FromBitmap(bm, 32, 10, 1000)
	want := []Extent{{1010, 20}, {1080, 80}, {1310, 10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// Short bitmap: missing bits count as used.
	got = FromBitmap([]byte{0}, 12, 1, 0)
	if !reflect.DeepEqual(got, []Extent{{8, 4}}) {
		t.Fatalf("short bitmap: %v", got)
	}
}
