package cluster

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

// ConsistentHash implements a consistent hashing ring for session affinity.
type ConsistentHash struct {
	mu       sync.RWMutex
	replicas int
	ring     []uint32          // sorted hash values
	nodes    map[uint32]string // hash -> node name
	members  map[string]bool   // set of real node names
}

// NewConsistentHash creates a new consistent hash ring with the given number
// of virtual replicas per node.
func NewConsistentHash(replicas int) *ConsistentHash {
	if replicas <= 0 {
		replicas = 100
	}
	return &ConsistentHash{
		replicas: replicas,
		nodes:    make(map[uint32]string),
		members:  make(map[string]bool),
	}
}

// AddNode adds a node to the hash ring with virtual replicas.
func (ch *ConsistentHash) AddNode(node string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if ch.members[node] {
		return
	}
	ch.members[node] = true

	for i := 0; i < ch.replicas; i++ {
		h := hashKey(node + ":" + strconv.Itoa(i))
		ch.ring = append(ch.ring, h)
		ch.nodes[h] = node
	}
	sort.Slice(ch.ring, func(i, j int) bool { return ch.ring[i] < ch.ring[j] })
}

// RemoveNode removes a node and all its virtual replicas from the ring.
func (ch *ConsistentHash) RemoveNode(node string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if !ch.members[node] {
		return
	}
	delete(ch.members, node)

	var newRing []uint32
	for _, h := range ch.ring {
		if ch.nodes[h] == node {
			delete(ch.nodes, h)
		} else {
			newRing = append(newRing, h)
		}
	}
	ch.ring = newRing
}

// GetNode returns the node responsible for the given session ID.
// Returns empty string if the ring is empty.
func (ch *ConsistentHash) GetNode(sessionID string) string {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.ring) == 0 {
		return ""
	}

	h := hashKey(sessionID)
	idx := sort.Search(len(ch.ring), func(i int) bool { return ch.ring[i] >= h })
	if idx >= len(ch.ring) {
		idx = 0
	}
	return ch.nodes[ch.ring[idx]]
}

// hashKey computes an FNV-1a 32-bit hash of the given key.
func hashKey(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}
