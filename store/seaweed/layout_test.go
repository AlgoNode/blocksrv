package seaweed

import (
	"testing"

	"github.com/algonode/blocksrv/store/rounds"
)

func TestBlockDataKey(t *testing.T) {
	cases := []struct {
		round uint64
		want  string
	}{
		{0, "blockdata/000/000/000/000000000000.msgp"},
		{999, "blockdata/000/000/000/000000000999.msgp"},
		{1000, "blockdata/000/000/001/000000001000.msgp"},
		{12_345_678, "blockdata/000/012/345/000012345678.msgp"},
		{999_999_999_999, "blockdata/999/999/999/999999999999.msgp"},
	}
	for _, c := range cases {
		got, err := BlockDataKey("", c.round)
		if err != nil || got != c.want {
			t.Fatalf("BlockDataKey(%d) = %q, %v; want %q", c.round, got, err, c.want)
		}
	}
	if _, err := BlockDataKey("", rounds.MaxRound); err == nil {
		t.Fatal("expected capacity error at MaxRound")
	}
}

func TestPrefixNormalisation(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "v1": "v1/", "/v1/": "v1/", " a/b ": "a/b/"} {
		if got := normalizePrefix(in); got != want {
			t.Fatalf("normalizePrefix(%q) = %q, want %q", in, got, want)
		}
	}
	got, _ := BlockDataKey("/v1/", 5)
	if got != "v1/blockdata/000/000/000/000000000005.msgp" {
		t.Fatalf("prefixed key = %q", got)
	}
	l := newLayout("v1")
	if l.genesisKey() != "v1/genesis.msgp" || l.latestKey() != "v1/latest.json" {
		t.Fatalf("genesis/latest keys = %q %q", l.genesisKey(), l.latestKey())
	}
	if k, err := l.metaKey("import"); err != nil || k != "v1/meta/import" {
		t.Fatalf("metaKey = %q, %v", k, err)
	}
	if _, err := l.metaKey("a/b"); err == nil {
		t.Fatal("meta key with slash must be rejected")
	}
}

func TestKeysSortNumerically(t *testing.T) {
	l := newLayout("")
	prev, _ := l.blockDataKey(0)
	for _, r := range []uint64{1, 9, 10, 999, 1000, 1001, 999_999, 1_000_000, 999_999_999, 1_000_000_000, 5_000_000_000} {
		k, _ := l.blockDataKey(r)
		if k <= prev {
			t.Fatalf("key for %d (%q) does not sort after %q", r, k, prev)
		}
		prev = k
	}
}

func TestParseBlockDataKey(t *testing.T) {
	l := newLayout("x/")
	for _, r := range []uint64{0, 7, 1000, 12_345_678, 999_999_999_999} {
		k, _ := l.blockDataKey(r)
		got, ok := l.parseBlockDataKey(k)
		if !ok || got != r {
			t.Fatalf("parse(%q) = %d, %v", k, got, ok)
		}
	}
	for _, bad := range []string{"blockdata/000/000/000/000000000001.msgp", "x/blockdata/000/000/001/000000000001.msgp", "x/blockdata/000/000/000/1.msgp", "x/genesis.msgp"} {
		if _, ok := l.parseBlockDataKey(bad); ok {
			t.Fatalf("parse(%q) must fail", bad)
		}
	}
}
