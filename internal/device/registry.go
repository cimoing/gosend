package device

import (
	"sort"
	"sync"
	"time"

	"gosend/internal/localsend"
)

const DefaultTTL = 90 * time.Second

type Device struct {
	Info     localsend.DeviceInfo `json:"info"`
	IP       string               `json:"ip"`
	LastSeen time.Time            `json:"lastSeen"`
}

type Registry struct {
	mu      sync.RWMutex
	devices map[string]Device
	ttl     time.Duration
	now     func() time.Time
}

func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Registry{
		devices: make(map[string]Device),
		ttl:     ttl,
		now:     time.Now,
	}
}

func (registry *Registry) Upsert(info localsend.DeviceInfo, ip string) bool {
	if info.Fingerprint == "" || info.Alias == "" || ip == "" {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_, existed := registry.devices[info.Fingerprint]
	registry.devices[info.Fingerprint] = Device{
		Info:     info,
		IP:       ip,
		LastSeen: registry.now().UTC(),
	}
	return !existed
}

func (registry *Registry) Get(fingerprint string) (Device, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	found, ok := registry.devices[fingerprint]
	return found, ok
}

func (registry *Registry) List() []Device {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	devices := make([]Device, 0, len(registry.devices))
	for _, found := range registry.devices {
		devices = append(devices, found)
	}
	sort.Slice(devices, func(left, right int) bool {
		if devices[left].Info.Alias == devices[right].Info.Alias {
			return devices[left].Info.Fingerprint < devices[right].Info.Fingerprint
		}
		return devices[left].Info.Alias < devices[right].Info.Alias
	})
	return devices
}

func (registry *Registry) RemoveExpired() []Device {
	cutoff := registry.now().UTC().Add(-registry.ttl)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var removed []Device
	for fingerprint, found := range registry.devices {
		if found.LastSeen.After(cutoff) {
			continue
		}
		removed = append(removed, found)
		delete(registry.devices, fingerprint)
	}
	return removed
}
