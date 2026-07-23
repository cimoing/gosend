package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"gosend/internal/config"
	"gosend/internal/store"
)

const (
	settingDeviceAlias   = "device.alias"
	settingDeviceModel   = "device.model"
	settingDeviceType    = "device.type"
	settingReceivePolicy = "receive.policy"
)

type editableSettings struct {
	Alias         string `json:"alias"`
	DeviceModel   string `json:"deviceModel"`
	DeviceType    string `json:"deviceType"`
	ReceivePolicy string `json:"receivePolicy"`
}

type runtimeSettings struct {
	mu      sync.RWMutex
	current editableSettings
}

func defaultEditableSettings(cfg config.Config) editableSettings {
	policy := strings.ToLower(strings.TrimSpace(cfg.ReceivePolicy))
	if policy == "" {
		policy = "manual"
	}
	return editableSettings{
		Alias:         cfg.Alias,
		DeviceModel:   "GoSend",
		DeviceType:    "server",
		ReceivePolicy: policy,
	}
}

func loadRuntimeSettings(
	ctx context.Context,
	database store.Store,
	cfg config.Config,
) (*runtimeSettings, error) {
	current := defaultEditableSettings(cfg)
	values := []struct {
		key    string
		target *string
	}{
		{settingDeviceAlias, &current.Alias},
		{settingDeviceModel, &current.DeviceModel},
		{settingDeviceType, &current.DeviceType},
		{settingReceivePolicy, &current.ReceivePolicy},
	}
	for _, value := range values {
		stored, err := database.GetSetting(ctx, value.key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", value.key, err)
		}
		*value.target = stored
	}
	normalized, err := normalizeEditableSettings(current)
	if err != nil {
		return nil, fmt.Errorf("validate persisted device settings: %w", err)
	}
	return &runtimeSettings{current: normalized}, nil
}

func (settings *runtimeSettings) Current() editableSettings {
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	return settings.current
}

func (settings *runtimeSettings) Update(
	ctx context.Context,
	database store.Store,
	input editableSettings,
) (editableSettings, error) {
	normalized, err := normalizeEditableSettings(input)
	if err != nil {
		return editableSettings{}, err
	}
	for _, value := range []struct {
		key   string
		value string
	}{
		{settingDeviceAlias, normalized.Alias},
		{settingDeviceModel, normalized.DeviceModel},
		{settingDeviceType, normalized.DeviceType},
		{settingReceivePolicy, normalized.ReceivePolicy},
	} {
		if err := database.SetSetting(ctx, value.key, value.value); err != nil {
			return editableSettings{}, fmt.Errorf("save %s: %w", value.key, err)
		}
	}
	settings.mu.Lock()
	settings.current = normalized
	settings.mu.Unlock()
	return normalized, nil
}

func normalizeEditableSettings(input editableSettings) (editableSettings, error) {
	input.Alias = strings.TrimSpace(input.Alias)
	input.DeviceModel = strings.TrimSpace(input.DeviceModel)
	input.DeviceType = strings.ToLower(strings.TrimSpace(input.DeviceType))
	input.ReceivePolicy = strings.ToLower(strings.TrimSpace(input.ReceivePolicy))
	if err := validateLabel("device alias", input.Alias, 80); err != nil {
		return editableSettings{}, err
	}
	if err := validateLabel("device model", input.DeviceModel, 80); err != nil {
		return editableSettings{}, err
	}
	switch input.DeviceType {
	case "mobile", "desktop", "web", "headless", "server":
	default:
		return editableSettings{}, errors.New("invalid device type")
	}
	switch input.ReceivePolicy {
	case "manual", "trusted", "auto":
	default:
		return editableSettings{}, errors.New("invalid receive policy")
	}
	return input, nil
}

func validateLabel(name, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s must not exceed %d characters", name, maximum)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains control characters", name)
	}
	return nil
}
