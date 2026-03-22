package nakama

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"

	"github.com/your-org/nakama-voice/cluster"
	"github.com/your-org/nakama-voice/config"
	"github.com/your-org/nakama-voice/metrics"
	"github.com/your-org/nakama-voice/moderation"
	"github.com/your-org/nakama-voice/room"
	"github.com/your-org/nakama-voice/sfu"
	"github.com/your-org/nakama-voice/signaling"
	"github.com/your-org/nakama-voice/storage"
	"github.com/your-org/nakama-voice/turn"
)

// RPCHandlers holds references to all subsystems needed by RPC endpoints.
type RPCHandlers struct {
	roomMgr      *room.Manager
	engine       *sfu.Engine
	cfg          *config.VoiceConfig
	voiceMetrics *metrics.VoiceMetrics
	moderator    *moderation.Moderator
	turnServer   *turn.Server
	coordinator  *cluster.Coordinator
	storageH     *StorageHelper
	recStore     storage.RecordingStore
	notifier     *Notifier
}

// NewRPCHandlers creates an RPC handler set with all subsystem references.
func NewRPCHandlers(
	roomMgr *room.Manager,
	engine *sfu.Engine,
	cfg *config.VoiceConfig,
	m *metrics.VoiceMetrics,
	mod *moderation.Moderator,
	ts *turn.Server,
	coord *cluster.Coordinator,
	sh *StorageHelper,
	recStore storage.RecordingStore,
	notifier *Notifier,
) *RPCHandlers {
	return &RPCHandlers{
		roomMgr:      roomMgr,
		engine:       engine,
		cfg:          cfg,
		voiceMetrics: m,
		moderator:    mod,
		turnServer:   ts,
		coordinator:  coord,
		storageH:     sh,
		recStore:     recStore,
		notifier:     notifier,
	}
}

// RegisterAll registers all 13 RPC endpoints with the Nakama initializer.
func (rpc *RPCHandlers) RegisterAll(initializer runtime.Initializer) error {
	rpcs := map[string]func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error){
		"voice_room_create":       rpc.VoiceRoomCreate,
		"voice_room_get":          rpc.VoiceRoomGet,
		"voice_room_list":         rpc.VoiceRoomList,
		"voice_room_delete":       rpc.VoiceRoomDelete,
		"voice_room_participants": rpc.VoiceRoomParticipants,
		"voice_turn_credentials":  rpc.VoiceTurnCredentials,
		"voice_moderation_mute":   rpc.VoiceModerationMute,
		"voice_moderation_kick":   rpc.VoiceModerationKick,
		"voice_moderation_ban":    rpc.VoiceModerationBan,
		"voice_recordings_list":   rpc.VoiceRecordingsList,
		"voice_recordings_delete": rpc.VoiceRecordingsDelete,
		"voice_metrics_get":       rpc.VoiceMetricsGet,
		"voice_config_update":     rpc.VoiceConfigUpdate,
	}

	for name, fn := range rpcs {
		if err := initializer.RegisterRpc(name, fn); err != nil {
			return fmt.Errorf("rpc: register %s: %w", name, err)
		}
	}
	return nil
}

// --- Request/Response Types ---

// CreateRoomReq is the request body for voice_room_create.
type CreateRoomReq struct {
	Label           string `json:"label"`
	MaxParticipants int    `json:"max_participants,omitempty"`
	ChannelType     string `json:"channel_type,omitempty"`
	SpatialEnabled  bool   `json:"spatial_enabled,omitempty"`
	AutoRecording   bool   `json:"auto_recording,omitempty"`
}

// CreateRoomResp is the response body for voice_room_create.
type CreateRoomResp struct {
	RoomID  string `json:"room_id"`
	MatchID string `json:"match_id"`
}

// GetRoomReq is the request body for voice_room_get.
type GetRoomReq struct {
	RoomID string `json:"room_id"`
}

// ListRoomsReq is the request body for voice_room_list.
type ListRoomsReq struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// DeleteRoomReq is the request body for voice_room_delete.
type DeleteRoomReq struct {
	RoomID string `json:"room_id"`
}

// ParticipantsReq is the request body for voice_room_participants.
type ParticipantsReq struct {
	RoomID string `json:"room_id"`
}

// MuteReq is the request body for voice_moderation_mute.
type MuteReq struct {
	RoomID          string `json:"room_id"`
	TargetUserID    string `json:"target_user_id"`
	Muted           bool   `json:"muted"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// KickReq is the request body for voice_moderation_kick.
type KickReq struct {
	RoomID          string `json:"room_id"`
	TargetUserID    string `json:"target_user_id"`
	Reason          string `json:"reason,omitempty"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
}

// BanReq is the request body for voice_moderation_ban.
type BanReq struct {
	RoomID          string `json:"room_id"`
	TargetUserID    string `json:"target_user_id"`
	Reason          string `json:"reason,omitempty"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
	Permanent       bool   `json:"permanent,omitempty"`
}

// RecordingsListReq is the request body for voice_recordings_list.
type RecordingsListReq struct {
	RoomID string `json:"room_id,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// RecordingsDeleteReq is the request body for voice_recordings_delete.
type RecordingsDeleteReq struct {
	RecordingID string `json:"recording_id"`
}

// ConfigUpdateReq is the request body for voice_config_update.
type ConfigUpdateReq struct {
	Overrides map[string]interface{} `json:"overrides"`
}

// SuccessResp is a generic success response.
type SuccessResp struct {
	Success bool `json:"success"`
}

// --- RPC Implementations ---

// VoiceRoomCreate creates a new voice room and its backing Nakama match.
func (rpc *RPCHandlers) VoiceRoomCreate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	var req CreateRoomReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}

	if req.Label == "" {
		req.Label = "voice-room"
	}
	if req.MaxParticipants <= 0 {
		req.MaxParticipants = rpc.cfg.GetRoom().MaxParticipants
	}

	// Create Nakama match
	matchParams := map[string]interface{}{
		"room_id":  "",
		"owner_id": userID,
		"label":    req.Label,
		"config":   rpc.cfg,
		"metrics":  rpc.voiceMetrics,
		"engine":   rpc.engine,
	}

	matchID, err := nk.MatchCreate(ctx, "voice", matchParams)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("match create failed: %v", err), 13)
	}

	roomID := matchID

	// Create room in room manager
	r, err := rpc.roomMgr.CreateRoom(roomID, matchID, req.Label, userID)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("room create failed: %v", err), 13)
	}

	// Persist metadata
	metadata := map[string]interface{}{
		"id":                roomID,
		"match_id":          matchID,
		"label":             req.Label,
		"owner_id":          userID,
		"max_participants":  req.MaxParticipants,
		"created_at":        r.CreatedAt.UnixMilli(),
		"spatial_enabled":   req.SpatialEnabled,
		"auto_recording":    req.AutoRecording,
	}
	if _, storeErr := rpc.storageH.WriteRoomMetadata(ctx, nk, roomID, metadata, ""); storeErr != nil {
		logger.Warn("rpc: failed to persist room metadata: %v", storeErr)
	}

	// Register in cluster
	if rpc.coordinator != nil {
		if regErr := rpc.coordinator.RegisterRoom(roomID); regErr != nil {
			logger.Warn("rpc: failed to register room in cluster: %v", regErr)
		}
	}

	resp := CreateRoomResp{RoomID: roomID, MatchID: matchID}
	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}

	logger.Info("rpc: room created room=%s match=%s owner=%s", roomID, matchID, userID)
	return string(data), nil
}

// VoiceRoomGet returns metadata and participants for a room.
func (rpc *RPCHandlers) VoiceRoomGet(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req GetRoomReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RoomID == "" {
		return "", runtime.NewError("room_id is required", 3)
	}

	r := rpc.roomMgr.GetRoom(req.RoomID)
	if r == nil {
		return "", runtime.NewError("room not found", 5)
	}

	resp := map[string]interface{}{
		"room_id":           r.ID,
		"match_id":          r.MatchID,
		"label":             r.Label,
		"owner_id":          r.OwnerID,
		"participant_count": r.ParticipantCount(),
		"participants":      r.ParticipantList(),
		"channels":          r.Channels().AllChannels(),
		"created_at":        r.CreatedAt.UnixMilli(),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceRoomList returns a list of active voice rooms.
func (rpc *RPCHandlers) VoiceRoomList(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req ListRoomsReq
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
		}
	}
	if req.Limit <= 0 {
		req.Limit = 100
	}

	rooms := rpc.roomMgr.ListRooms(req.Limit)
	roomList := make([]map[string]interface{}, 0, len(rooms))
	for _, r := range rooms {
		roomList = append(roomList, map[string]interface{}{
			"room_id":           r.ID,
			"label":             r.Label,
			"owner_id":          r.OwnerID,
			"participant_count": r.ParticipantCount(),
			"created_at":        r.CreatedAt.UnixMilli(),
		})
	}

	resp := map[string]interface{}{
		"rooms":  roomList,
		"cursor": "",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceRoomDelete removes a voice room. Requires owner or admin role.
func (rpc *RPCHandlers) VoiceRoomDelete(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	var req DeleteRoomReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RoomID == "" {
		return "", runtime.NewError("room_id is required", 3)
	}

	r := rpc.roomMgr.GetRoom(req.RoomID)
	if r == nil {
		return "", runtime.NewError("room not found", 5)
	}

	// Check permission: only owner or admin
	if r.OwnerID != userID && !rpc.cfg.IsAdmin(userID) {
		return "", runtime.NewError("permission denied: only owner or admin can delete rooms", 7)
	}

	// Destroy SFU room
	if rpc.engine != nil {
		rpc.engine.DestroyRoom(req.RoomID)
	}

	// Remove from registry
	rpc.roomMgr.DestroyRoom(req.RoomID)

	// Remove from storage
	if delErr := rpc.storageH.DeleteRoomMetadata(ctx, nk, req.RoomID); delErr != nil {
		logger.Warn("rpc: failed to delete room metadata: %v", delErr)
	}

	// Unregister from cluster
	if rpc.coordinator != nil {
		_ = rpc.coordinator.UnregisterRoom(req.RoomID)
	}

	logger.Info("rpc: room deleted room=%s by=%s", req.RoomID, userID)

	data, err := json.Marshal(SuccessResp{Success: true})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceRoomParticipants returns the participant list for a room.
func (rpc *RPCHandlers) VoiceRoomParticipants(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req ParticipantsReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RoomID == "" {
		return "", runtime.NewError("room_id is required", 3)
	}

	r := rpc.roomMgr.GetRoom(req.RoomID)
	if r == nil {
		return "", runtime.NewError("room not found", 5)
	}

	resp := map[string]interface{}{
		"participants": r.ParticipantList(),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceTurnCredentials generates time-limited TURN credentials for the caller.
func (rpc *RPCHandlers) VoiceTurnCredentials(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	if rpc.turnServer == nil || !rpc.turnServer.IsEnabled() {
		// Return STUN-only servers
		sfuCfg := rpc.cfg.GetSFU()
		servers := make([]signaling.IceServerInfo, 0, len(sfuCfg.STUNServers))
		for _, url := range sfuCfg.STUNServers {
			servers = append(servers, signaling.IceServerInfo{URLs: []string{url}})
		}
		resp := map[string]interface{}{
			"servers": servers,
			"ttl":     0,
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
		}
		return string(data), nil
	}

	cred := rpc.turnServer.GenerateCredentials(userID)

	resp := map[string]interface{}{
		"servers": []signaling.IceServerInfo{
			{
				URLs:       cred.URLs,
				Username:   cred.Username,
				Credential: cred.Password,
			},
		},
		"ttl": cred.TTL,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceModerationMute mutes or unmutes a user in a room.
func (rpc *RPCHandlers) VoiceModerationMute(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	var req MuteReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RoomID == "" || req.TargetUserID == "" {
		return "", runtime.NewError("room_id and target_user_id are required", 3)
	}

	r := rpc.roomMgr.GetRoom(req.RoomID)
	if r == nil {
		return "", runtime.NewError("room not found", 5)
	}

	if err := rpc.moderator.MuteUser(ctx, logger, nk, r, userID, req.TargetUserID, req.Muted, req.DurationSeconds, req.Reason); err != nil {
		return "", runtime.NewError(fmt.Sprintf("mute failed: %v", err), 7)
	}

	// Send notification
	_ = rpc.notifier.NotifyMute(ctx, nk, req.RoomID, req.TargetUserID, req.Muted)

	data, err := json.Marshal(SuccessResp{Success: true})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceModerationKick removes a user from a room with optional cooldown.
func (rpc *RPCHandlers) VoiceModerationKick(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	var req KickReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RoomID == "" || req.TargetUserID == "" {
		return "", runtime.NewError("room_id and target_user_id are required", 3)
	}

	r := rpc.roomMgr.GetRoom(req.RoomID)
	if r == nil {
		return "", runtime.NewError("room not found", 5)
	}

	if err := rpc.moderator.KickUser(ctx, logger, nk, r, userID, req.TargetUserID, req.Reason, req.CooldownSeconds); err != nil {
		return "", runtime.NewError(fmt.Sprintf("kick failed: %v", err), 7)
	}

	// Remove peer from SFU
	if rpc.engine != nil {
		rpc.engine.RemovePeer(req.RoomID, req.TargetUserID)
	}

	// Send notification
	_ = rpc.notifier.NotifyKick(ctx, nk, req.RoomID, req.TargetUserID, req.Reason, req.CooldownSeconds)

	data, err := json.Marshal(SuccessResp{Success: true})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceModerationBan bans a user from a room.
func (rpc *RPCHandlers) VoiceModerationBan(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	var req BanReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RoomID == "" || req.TargetUserID == "" {
		return "", runtime.NewError("room_id and target_user_id are required", 3)
	}

	r := rpc.roomMgr.GetRoom(req.RoomID)
	if r == nil {
		return "", runtime.NewError("room not found", 5)
	}

	if err := rpc.moderator.BanUser(ctx, logger, nk, r, userID, req.TargetUserID, req.Reason, req.DurationSeconds, req.Permanent); err != nil {
		return "", runtime.NewError(fmt.Sprintf("ban failed: %v", err), 7)
	}

	// Store ban record
	banRecord := map[string]interface{}{
		"user_id":    req.TargetUserID,
		"room_id":    req.RoomID,
		"banned_by":  userID,
		"reason":     req.Reason,
		"created_at": time.Now().UnixMilli(),
		"permanent":  req.Permanent,
	}
	if !req.Permanent && req.DurationSeconds > 0 {
		banRecord["expires_at"] = time.Now().Add(time.Duration(req.DurationSeconds) * time.Second).Unix()
	}
	if storeErr := rpc.storageH.WriteBanRecord(ctx, nk, req.RoomID, req.TargetUserID, banRecord); storeErr != nil {
		logger.Warn("rpc: failed to store ban record: %v", storeErr)
	}

	// Remove peer from SFU
	if rpc.engine != nil {
		rpc.engine.RemovePeer(req.RoomID, req.TargetUserID)
	}

	// Send notification
	_ = rpc.notifier.NotifyBan(ctx, nk, req.RoomID, req.TargetUserID, req.Reason, req.DurationSeconds)

	data, err := json.Marshal(SuccessResp{Success: true})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceRecordingsList lists recording metadata.
func (rpc *RPCHandlers) VoiceRecordingsList(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req RecordingsListReq
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
		}
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}

	recordings, nextCursor, err := rpc.storageH.ListRecordings(ctx, nk, req.Limit, req.Cursor)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("list recordings: %v", err), 13)
	}

	resp := map[string]interface{}{
		"recordings": recordings,
		"cursor":     nextCursor,
	}

	data, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", marshalErr), 13)
	}
	return string(data), nil
}

// VoiceRecordingsDelete deletes a recording.
func (rpc *RPCHandlers) VoiceRecordingsDelete(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	var req RecordingsDeleteReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}
	if req.RecordingID == "" {
		return "", runtime.NewError("recording_id is required", 3)
	}

	// Delete from storage backend
	if rpc.recStore != nil {
		if delErr := rpc.recStore.Delete(ctx, req.RecordingID); delErr != nil {
			logger.Warn("rpc: failed to delete recording from backend: %v", delErr)
		}
	}

	// Delete metadata from Nakama storage
	if delErr := nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{Collection: "voice_recordings", Key: req.RecordingID, UserID: ""},
	}); delErr != nil {
		return "", runtime.NewError(fmt.Sprintf("delete recording metadata: %v", delErr), 13)
	}

	logger.Info("rpc: recording deleted id=%s by=%s", req.RecordingID, userID)

	data, err := json.Marshal(SuccessResp{Success: true})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceMetricsGet returns current voice module metrics. Accessible to all authenticated users.
func (rpc *RPCHandlers) VoiceMetricsGet(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	_, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", 16)
	}

	activeRooms := rpc.roomMgr.RoomCount()
	peerCount := 0
	if rpc.engine != nil {
		peerCount = rpc.engine.PeerCount()
	}
	clusterNodes := 0
	if rpc.coordinator != nil {
		clusterNodes = len(rpc.coordinator.ActiveNodes())
	}

	resp := map[string]interface{}{
		"active_rooms":        activeRooms,
		"active_participants": peerCount,
		"active_recordings":   0,
		"cluster_nodes":       clusterNodes,
		"metrics": map[string]interface{}{
			"sfu_rooms": func() int {
				if rpc.engine != nil {
					return rpc.engine.RoomCount()
				}
				return 0
			}(),
			"sfu_peers": peerCount,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}

// VoiceConfigUpdate updates the voice module configuration. Admin only.
func (rpc *RPCHandlers) VoiceConfigUpdate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}

	if !rpc.cfg.IsAdmin(userID) {
		return "", runtime.NewError("admin role required", 7)
	}

	var req ConfigUpdateReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError(fmt.Sprintf("invalid request: %v", err), 3)
	}

	if len(req.Overrides) == 0 {
		return "", runtime.NewError("overrides cannot be empty", 3)
	}

	// Write overrides to storage
	if err := rpc.storageH.WriteConfig(ctx, nk, "global", req.Overrides); err != nil {
		return "", runtime.NewError(fmt.Sprintf("write config: %v", err), 13)
	}

	// Hot-reload config
	if err := rpc.cfg.Reload(ctx, logger, nk); err != nil {
		logger.Warn("rpc: config hot-reload failed: %v", err)
	}

	logger.Info("rpc: config updated by admin=%s", userID)

	data, err := json.Marshal(SuccessResp{Success: true})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("marshal response: %v", err), 13)
	}
	return string(data), nil
}