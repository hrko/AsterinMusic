package audio

import (
	"errors"
	"reflect"
	"testing"
)

func TestBitrateChangesAndMissingValues(t *testing.T) {
	value := 64000
	b := bitrateControl{read: func() int { return value }}
	var got []int
	set := func(v int) error { got = append(got, v); return nil }
	for _, valueNow := range []int{64000, 64000, 128000, 0, 384000, 8000, -1, 999999} {
		value = valueNow
		if err := b.apply(set); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, []int{64000, 128000, 384000, 8000}) {
		t.Fatal(got)
	}
}

func TestBitrateDefaultAndFailedUpdate(t *testing.T) {
	b := bitrateControl{}
	if err := b.apply(func(v int) error {
		if v != 96000 {
			t.Fatal(v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.read = func() int { return 64000 }
	failure := errors.New("rejected")
	if err := b.apply(func(int) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if b.applied != 96000 {
		t.Fatal(b.applied)
	}
	if err := b.apply(func(v int) error {
		if v != 64000 {
			t.Fatal(v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
