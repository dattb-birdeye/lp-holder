package lpsource

import (
	"fmt"
	"testing"
)

// ShardFor must stay identical to token-holder's ShardManager routing
// (cityHash64(key) % shards), otherwise reads go to a shard without data.
func TestShardForIsDeterministicAndSpread(t *testing.T) {
	const shards = 4
	counts := make([]int, shards)
	for i := range 1000 {
		key := fmt.Sprintf("mint-%d", i)
		s := ShardFor(key, shards)
		if s != ShardFor(key, shards) {
			t.Fatal("routing must be deterministic")
		}
		counts[s]++
	}
	for i, c := range counts {
		if c < 150 {
			t.Errorf("shard %d got %d of 1000 keys; hash looks broken", i, c)
		}
	}
	if ShardFor("x", 1) != 0 {
		t.Fatal("single shard must route to 0")
	}
}
