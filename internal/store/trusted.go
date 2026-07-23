package store

import (
	"sort"

	"gosend/internal/domain"
	"gosend/internal/localsend"
)

func deduplicateTrustedDevices(devices []domain.TrustedDevice) []domain.TrustedDevice {
	byFingerprint := make(map[string]domain.TrustedDevice, len(devices))
	for _, found := range devices {
		found.Fingerprint = localsend.NormalizeFingerprint(found.Fingerprint)
		existing, ok := byFingerprint[found.Fingerprint]
		if !ok {
			byFingerprint[found.Fingerprint] = found
			continue
		}
		preferred := existing
		if found.UpdatedAt.After(existing.UpdatedAt) {
			preferred = found
		}
		if !found.CreatedAt.IsZero() &&
			(preferred.CreatedAt.IsZero() || found.CreatedAt.Before(preferred.CreatedAt)) {
			preferred.CreatedAt = found.CreatedAt
		}
		if !existing.CreatedAt.IsZero() &&
			(preferred.CreatedAt.IsZero() || existing.CreatedAt.Before(preferred.CreatedAt)) {
			preferred.CreatedAt = existing.CreatedAt
		}
		byFingerprint[found.Fingerprint] = preferred
	}
	result := make([]domain.TrustedDevice, 0, len(byFingerprint))
	for _, found := range byFingerprint {
		result = append(result, found)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Alias == result[right].Alias {
			return result[left].Fingerprint < result[right].Fingerprint
		}
		return result[left].Alias < result[right].Alias
	})
	return result
}
