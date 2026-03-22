package moderation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/heroiclabs/nakama-common/runtime"
)

// AuditEntry represents a single entry in the append-only audit log.
type AuditEntry struct {
	ID        string            `json:"id"`
	RoomID    string            `json:"room_id"`
	ActorID   string            `json:"actor_id"`
	TargetID  string            `json:"target_id"`
	Action    string            `json:"action"`
	Reason    string            `json:"reason"`
	Timestamp int64             `json:"timestamp"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AuditLog manages the append-only audit log stored in Nakama storage.
type AuditLog struct{}

// NewAuditLog creates a new audit log writer.
func NewAuditLog() *AuditLog {
	return &AuditLog{}
}

// Write appends an audit entry to the voice_audit_log collection.
// Each entry is stored with a unique key to ensure append-only semantics.
func (al *AuditLog) Write(ctx context.Context, nk runtime.NakamaModule, entry AuditEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit: marshal entry: %w", err)
	}

	key := fmt.Sprintf("%d:%s", entry.Timestamp, entry.ID)

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "voice_audit_log",
			Key:             key,
			UserID:          "",
			Value:           string(data),
			PermissionRead:  0, // System only
			PermissionWrite: 0, // System only
		},
	})
	if err != nil {
		return fmt.Errorf("audit: write entry: %w", err)
	}

	return nil
}

// Read retrieves audit log entries for a room, optionally filtered by time range.
func (al *AuditLog) Read(ctx context.Context, nk runtime.NakamaModule, roomID string, limit int) ([]AuditEntry, error) {
	objs, _, err := nk.StorageList(ctx, "", "", "voice_audit_log", limit, "")
	if err != nil {
		return nil, fmt.Errorf("audit: list entries: %w", err)
	}

	entries := make([]AuditEntry, 0, len(objs))
	for _, obj := range objs {
		var entry AuditEntry
		if parseErr := json.Unmarshal([]byte(obj.Value), &entry); parseErr != nil {
			continue
		}
		if roomID == "" || entry.RoomID == roomID {
			entries = append(entries, entry)
		}
	}

	return entries, nil
}
