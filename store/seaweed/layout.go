package seaweed

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/algonode/blocksrv/store/rounds"
)

// Object layout inside a bucket (one bucket per network):
//
//	<prefix>genesis.msgp                              raw genesis msgpack
//	<prefix>latest.json                               {"last-round":N,...}, Cache-Control: no-cache
//	<prefix>blockdata/AAA/BBB/CCC/AAABBBCCCDDD.msgp   raw BlockData for round AAABBBCCCDDD
//	<prefix>meta/<key>                                small opaque values (import progress, ...)
//
// Directories hold at most 1000 entries per level and listings sort numerically.
const (
	blockDataDir = "blockdata"
	blockDataExt = ".msgp"
	genesisName  = "genesis.msgp"
	latestName   = "latest.json"
	metaDir      = "meta"

	ContentTypeMsgpack = "application/msgpack"
	ContentTypeJSON    = "application/json"
	ContentTypeBinary  = "application/octet-stream"
	CacheImmutable     = "public, max-age=31536000, immutable"
	CacheNoCache       = "no-cache"

	// MetaRound is the user metadata key carrying the round (x-amz-meta-round on the wire).
	MetaRound = "round"
)

type layout struct {
	prefix string // "" or "a/b/"
}

func newLayout(prefix string) layout {
	return layout{prefix: normalizePrefix(prefix)}
}

func normalizePrefix(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// BlockDataKey returns the object key holding the raw BlockData of round.
func BlockDataKey(prefix string, round uint64) (string, error) {
	return newLayout(prefix).blockDataKey(round)
}

func (l layout) blockDataKey(round uint64) (string, error) {
	if round >= rounds.MaxRound {
		return "", fmt.Errorf("round %d exceeds layout capacity %d", round, rounds.MaxRound)
	}
	return fmt.Sprintf("%s%s/%03d/%03d/%03d/%012d%s",
		l.prefix, blockDataDir,
		round/1_000_000_000, (round/1_000_000)%1000, (round/1000)%1000,
		round, blockDataExt), nil
}

// parseBlockDataKey is the inverse of blockDataKey.
func (l layout) parseBlockDataKey(key string) (uint64, bool) {
	rest, ok := strings.CutPrefix(key, l.prefix+blockDataDir+"/")
	if !ok {
		return 0, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return 0, false
	}
	name, ok := strings.CutSuffix(parts[3], blockDataExt)
	if !ok || len(name) != 12 {
		return 0, false
	}
	round, err := strconv.ParseUint(name, 10, 64)
	if err != nil {
		return 0, false
	}
	want, err := l.blockDataKey(round)
	if err != nil || want != key {
		return 0, false
	}
	return round, true
}

func (l layout) genesisKey() string { return l.prefix + genesisName }
func (l layout) latestKey() string  { return l.prefix + latestName }

func (l layout) metaKey(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("invalid meta key %q", name)
	}
	return l.prefix + metaDir + "/" + name, nil
}
