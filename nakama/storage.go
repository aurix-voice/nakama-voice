// Package nakama implements Nakama integration including RPCs, match handler,
// storage helpers, notifications, presence, and hooks.
package nakama

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// StorageHelper provides typed read and write functions for all five
// Nakama storage collections used by the voice module.
type StorageHelper struct{}

// NewStorageHelper creates a storage helper.
func NewStorageHelper() *StorageHelper {
	return &StorageHelper{}
}

// WriteRoomMetadata persists room metadata to the voice_rooms collection
// with version-checked conditional writes.
func (sh *StorageHelper) WriteRoomMetadata(ctx context.Context, nk runtime.NakamaModule, roomID string, metadata interface{}, version string) (string, error) {
	data, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("storage: marshal room metadata: %w", err)
	}

	write := &runtime.StorageWrite{
		Collection:      "voice_rooms",
		Key:             roomID,
		UserID:          "",
		Value:           string(data),
		Version:         version,
		PermissionRead:  2, // Public read
		PermissionWrite: 0, // System write
	}

	acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{write})
	if err != nil {
		return "", fmt.Errorf("storage: write room metadata: %w", err)
	}
	if len(acks) == 0 {
		return "", fmt.Errorf("storage: no ack for room metadata write")
	}

	return acks[0].Version, nil
}

// ReadRoomMetadata reads room metadata from storage.
func (sh *StorageHelper) ReadRoomMetadata(ctx context.Context, nk runtime.NakamaModule, roomID string) (json.RawMessage, string, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: "voice_rooms", Key: roomID, UserID: ""},
	})
	if err != nil {
		return nil, "", fmt.Errorf("storage: read room metadata: %w", err)
	}
	if len(objs) == 0 {
		return nil, "", nil
	}

	return json.RawMessage(objs[0].Value), objs[0].Version, nil
}

// DeleteRoomMetadata removes room metadata from storage.
func (sh *StorageHelper) DeleteRoomMetadata(ctx context.Context, nk runtime.NakamaModule, roomID string) error {
	return nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{Collection: "voice_rooms", Key: roomID, UserID: ""},
	})
}

// WriteBanRecord writes a ban record to the voice_bans collection.
func (sh *StorageHelper) WriteBanRecord(ctx context.Context, nk runtime.NakamaModule, roomID, userID string, banData interface{}) error {
	data, err := json.Marshal(banData)
	if err != nil {
		return fmt.Errorf("storage: marshal ban record: %w", err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "voice_bans",
			Key:             fmt.Sprintf("%s:%s", roomID, userID),
			UserID:          "",
			Value:           string(data),
			PermissionRead:  0, // System only
			PermissionWrite: 0,
		},
	})
	if err != nil {
		return fmt.Errorf("storage: write ban record: %w", err)
	}

	return nil
}

// ReadBanRecord reads a ban record from storage.
func (sh *StorageHelper) ReadBanRecord(ctx context.Context, nk runtime.NakamaModule, roomID, userID string) (json.RawMessage, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: "voice_bans", Key: fmt.Sprintf("%s:%s", roomID, userID), UserID: ""},
	})
	if err != nil {
		return nil, fmt.Errorf("storage: read ban: %w", err)
	}
	if len(objs) == 0 {
		return nil, nil
	}
	return json.RawMessage(objs[0].Value), nil
}

// WriteRecordingMetadata persists recording metadata.
func (sh *StorageHelper) WriteRecordingMetadata(ctx context.Context, nk runtime.NakamaModule, recordingID string, metadata interface{}) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("storage: marshal recording metadata: %w", err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "voice_recordings",
			Key:             recordingID,
			UserID:          "",
			Value:           string(data),
			PermissionRead:  1, // Owner read
			PermissionWrite: 0,
		},
	})
	if err != nil {
		return fmt.Errorf("storage: write recording metadata: %w", err)
	}

	return nil
}

// ListRecordings lists recording metadata, optionally filtered by room.
func (sh *StorageHelper) ListRecordings(ctx context.Context, nk runtime.NakamaModule, limit int, cursor string) ([]json.RawMessage, string, error) {
	objs, nextCursor, err := nk.StorageList(ctx, "", "", "voice_recordings", limit, cursor)
	if err != nil {
		return nil, "", fmt.Errorf("storage: list recordings: %w", err)
	}

	results := make([]json.RawMessage, 0, len(objs))
	for _, obj := range objs {
		results = append(results, json.RawMessage(obj.Value))
	}

	return results, nextCursor, nil
}

// WriteConfig writes a config override to the voice_config collection.
func (sh *StorageHelper) WriteConfig(ctx context.Context, nk runtime.NakamaModule, key string, configData interface{}) error {
	data, err := json.Marshal(configData)
	if err != nil {
		return fmt.Errorf("storage: marshal config: %w", err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "voice_config",
			Key:             key,
			UserID:          "",
			Value:           string(data),
			PermissionRead:  0, // System only
			PermissionWrite: 0,
		},
	})
	if err != nil {
		return fmt.Errorf("storage: write config: %w", err)
	}

	return nil
}
