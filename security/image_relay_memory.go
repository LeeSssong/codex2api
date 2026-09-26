package security

import "sync"

// Reads and conversions share a fail-fast concurrency ceiling, while bytes are
// charged to the native HTTP/WS request memory pool, never a separate queue.
var imageRelaySlots = make(chan struct{}, 32)

type ImageRelayMemoryReservation struct {
	memory *RequestMemoryReservation
	once   sync.Once
}

func TryAcquireImageRelayMemory(size int64) (*ImageRelayMemoryReservation, bool) {
	select {
	case imageRelaySlots <- struct{}{}:
	default:
		return nil, false
	}
	r, ok := TryAcquireRequestMemory(size)
	if !ok {
		<-imageRelaySlots
		return nil, false
	}
	return &ImageRelayMemoryReservation{memory: r}, true
}
func (r *ImageRelayMemoryReservation) Release() {
	if r == nil {
		return
	}
	r.once.Do(func() { r.memory.Release(); <-imageRelaySlots })
}
func ImageRelayActiveSlots() int { return len(imageRelaySlots) }
