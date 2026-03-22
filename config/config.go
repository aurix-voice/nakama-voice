package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/heroiclabs/nakama-common/runtime"
)

// VoiceConfig holds all configuration for the voice chat module.
// Configuration loads in priority order: compiled defaults → environment
// variables (NAKAMA_VOICE_ prefix) → Nakama storage overrides.
type VoiceConfig struct {
	SFU      SFUConfig      `json:"sfu"`
	Audio    AudioConfig    `json:"audio"`
	Room     RoomDefaults   `json:"room"`
	Cluster  ClusterConfig  `json:"cluster"`
	Storage  StorageConfig  `json:"storage"`
	Metrics  MetricsConfig  `json:"metrics"`
	Security SecurityConfig `json:"security"`
	TURN     TURNConfig     `json:"turn"`

	// mu protects hot-reloaded fields from concurrent access.
	mu sync.RWMutex
}

// SFUConfig holds SFU engine settings.
type SFUConfig struct {
	ListenAddress        string           `json:"listen_address"`
	ExternalAddress      string           `json:"external_address"`
	UDPPortMin           uint16           `json:"udp_port_min"`
	UDPPortMax           uint16           `json:"udp_port_max"`
	MaxPeerConnections   int              `json:"max_peer_connections"`
	ICETimeout           int              `json:"ice_timeout_s"`
	ICEKeepalive         int              `json:"ice_keepalive_s"`
	STUNServers          []string         `json:"stun_servers"`
	TURNServers          []TURNServerConfig `json:"turn_servers"`
	SimulcastEnabled     bool             `json:"simulcast_enabled"`
	REDEnabled           bool             `json:"red_enabled"`
	DTXEnabled           bool             `json:"dtx_enabled"`
	RTCPFeedbackInterval int              `json:"rtcp_feedback_interval_s"`
	MaxBandwidthKbps     int              `json:"max_bandwidth_kbps"`
}

// TURNServerConfig describes an external TURN server.
type TURNServerConfig struct {
	URL        string `json:"url"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

// AudioConfig holds audio processing settings.
type AudioConfig struct {
	VADAggressiveness int     `json:"vad_aggressiveness"`
	VADHangoverMs     int     `json:"vad_hangover_ms"`
	AGCTargetDBFS     int     `json:"agc_target_dbfs"`
	NoiseSuppression  int     `json:"noise_suppression_level"`
	SpatialEnabled    bool    `json:"spatial_enabled"`
	HRTFEnabled       bool    `json:"hrtf_enabled"`
	MaxSpatialDistance float64 `json:"max_spatial_distance_m"`
	AttenuationModel  string  `json:"attenuation_model"`
}

// RoomDefaults holds default room settings.
type RoomDefaults struct {
	MaxParticipants    int    `json:"max_participants"`
	DefaultChannelType string `json:"default_channel_type"`
	AutoRecording      bool   `json:"auto_recording"`
}

// ClusterConfig holds Redis cluster coordination settings.
type ClusterConfig struct {
	RedisAddress       string `json:"redis_address"`
	RedisPassword      string `json:"redis_password"`
	RedisDB            int    `json:"redis_db"`
	PoolMinConns       int    `json:"pool_min_conns"`
	PoolMaxConns       int    `json:"pool_max_conns"`
	HeartbeatIntervalS int    `json:"heartbeat_interval_s"`
	BridgeUDPPortMin   uint16 `json:"bridge_udp_port_min"`
	BridgeUDPPortMax   uint16 `json:"bridge_udp_port_max"`
}

// StorageConfig holds recording storage settings.
type StorageConfig struct {
	RecordingBackend  string `json:"recording_backend"`
	S3Bucket          string `json:"s3_bucket"`
	S3Region          string `json:"s3_region"`
	GCSBucket         string `json:"gcs_bucket"`
	EncryptionEnabled bool   `json:"encryption_enabled"`
	LocalPath         string `json:"local_path"`
}

// MetricsConfig holds Prometheus and health check settings.
type MetricsConfig struct {
	PrometheusAddress string `json:"prometheus_address"`
	HealthCheckPort   int    `json:"health_check_port"`
}

// SecurityConfig holds security-related settings.
type SecurityConfig struct {
	AdminUserIDs     []string `json:"admin_user_ids"`
	BanAppealTTLDays int      `json:"ban_appeal_ttl_days"`
}

// TURNConfig holds built-in TURN server settings.
type TURNConfig struct {
	Enabled          bool   `json:"enabled"`
	ListenAddress    string `json:"listen_address"`
	TLSListenAddress string `json:"tls_listen_address"`
	Realm            string `json:"realm"`
	CredentialTTL    int    `json:"credential_ttl_s"`
	MaxAllocations   int    `json:"max_allocations"`
	MaxBandwidthKbps int    `json:"max_bandwidth_kbps"`
}

// Load creates a VoiceConfig by applying compiled defaults, then
// environment variable overrides, then Nakama storage overrides.
// Returns an error if environment values are malformed.
func Load(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (*VoiceConfig, error) {
	cfg := DefaultConfig()

	if err := cfg.applyEnvOverrides(); err != nil {
		return nil, fmt.Errorf("config: env override failed: %w", err)
	}

	if err := cfg.applyStorageOverrides(ctx, logger, nk); err != nil {
		logger.Warn("config: storage override failed, using env/defaults: %v", err)
	}

	return cfg, nil
}

// Reload re-reads Nakama storage overrides and applies them. This supports
// hot-reload without process restart. Thread-safe via internal mutex.
func (c *VoiceConfig) Reload(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.applyStorageOverrides(ctx, logger, nk); err != nil {
		return fmt.Errorf("config: reload failed: %w", err)
	}
	logger.Info("config: reloaded from storage")
	return nil
}

// GetAudio returns the audio config with read lock protection.
func (c *VoiceConfig) GetAudio() AudioConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Audio
}

// GetSFU returns the SFU config with read lock protection.
func (c *VoiceConfig) GetSFU() SFUConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SFU
}

// GetRoom returns the room defaults config with read lock protection.
func (c *VoiceConfig) GetRoom() RoomDefaults {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Room
}

// GetCluster returns the cluster config with read lock protection.
func (c *VoiceConfig) GetCluster() ClusterConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Cluster
}

// GetSecurity returns the security config with read lock protection.
func (c *VoiceConfig) GetSecurity() SecurityConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Security
}

// GetTURN returns the TURN config with read lock protection.
func (c *VoiceConfig) GetTURN() TURNConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.TURN
}

// IsAdmin returns true if the given user ID is in the admin list.
func (c *VoiceConfig) IsAdmin(userID string) bool {
	sec := c.GetSecurity()
	for _, id := range sec.AdminUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (c *VoiceConfig) applyEnvOverrides() error {
	if v := os.Getenv("NAKAMA_VOICE_SFU_LISTEN_ADDRESS"); v != "" {
		c.SFU.ListenAddress = v
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_EXTERNAL_ADDRESS"); v != "" {
		c.SFU.ExternalAddress = v
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_UDP_PORT_MIN"); v != "" {
		p, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return fmt.Errorf("invalid SFU_UDP_PORT_MIN: %w", err)
		}
		c.SFU.UDPPortMin = uint16(p)
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_UDP_PORT_MAX"); v != "" {
		p, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return fmt.Errorf("invalid SFU_UDP_PORT_MAX: %w", err)
		}
		c.SFU.UDPPortMax = uint16(p)
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_MAX_PEERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SFU_MAX_PEERS: %w", err)
		}
		c.SFU.MaxPeerConnections = n
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_MAX_BANDWIDTH_KBPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SFU_MAX_BANDWIDTH_KBPS: %w", err)
		}
		c.SFU.MaxBandwidthKbps = n
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_SIMULCAST"); v != "" {
		c.SFU.SimulcastEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_RED"); v != "" {
		c.SFU.REDEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_DTX"); v != "" {
		c.SFU.DTXEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_SFU_STUN_SERVERS"); v != "" {
		c.SFU.STUNServers = strings.Split(v, ",")
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_VAD_AGGRESSIVENESS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid AUDIO_VAD_AGGRESSIVENESS: %w", err)
		}
		c.Audio.VADAggressiveness = n
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_VAD_HANGOVER_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid AUDIO_VAD_HANGOVER_MS: %w", err)
		}
		c.Audio.VADHangoverMs = n
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_AGC_TARGET"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid AUDIO_AGC_TARGET: %w", err)
		}
		c.Audio.AGCTargetDBFS = n
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_SPATIAL"); v != "" {
		c.Audio.SpatialEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_HRTF"); v != "" {
		c.Audio.HRTFEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_MAX_DISTANCE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("invalid AUDIO_MAX_DISTANCE: %w", err)
		}
		c.Audio.MaxSpatialDistance = f
	}
	if v := os.Getenv("NAKAMA_VOICE_AUDIO_ATTENUATION_MODEL"); v != "" {
		c.Audio.AttenuationModel = v
	}
	if v := os.Getenv("NAKAMA_VOICE_ROOM_MAX_PARTICIPANTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid ROOM_MAX_PARTICIPANTS: %w", err)
		}
		c.Room.MaxParticipants = n
	}
	if v := os.Getenv("NAKAMA_VOICE_ROOM_DEFAULT_CHANNEL"); v != "" {
		c.Room.DefaultChannelType = v
	}
	if v := os.Getenv("NAKAMA_VOICE_ROOM_AUTO_RECORDING"); v != "" {
		c.Room.AutoRecording = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_CLUSTER_REDIS_ADDRESS"); v != "" {
		c.Cluster.RedisAddress = v
	}
	if v := os.Getenv("NAKAMA_VOICE_CLUSTER_REDIS_PASSWORD"); v != "" {
		c.Cluster.RedisPassword = v
	}
	if v := os.Getenv("NAKAMA_VOICE_CLUSTER_REDIS_DB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid CLUSTER_REDIS_DB: %w", err)
		}
		c.Cluster.RedisDB = n
	}
	if v := os.Getenv("NAKAMA_VOICE_STORAGE_BACKEND"); v != "" {
		c.Storage.RecordingBackend = v
	}
	if v := os.Getenv("NAKAMA_VOICE_STORAGE_S3_BUCKET"); v != "" {
		c.Storage.S3Bucket = v
	}
	if v := os.Getenv("NAKAMA_VOICE_STORAGE_S3_REGION"); v != "" {
		c.Storage.S3Region = v
	}
	if v := os.Getenv("NAKAMA_VOICE_STORAGE_GCS_BUCKET"); v != "" {
		c.Storage.GCSBucket = v
	}
	if v := os.Getenv("NAKAMA_VOICE_STORAGE_ENCRYPTION"); v != "" {
		c.Storage.EncryptionEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_STORAGE_LOCAL_PATH"); v != "" {
		c.Storage.LocalPath = v
	}
	if v := os.Getenv("NAKAMA_VOICE_METRICS_PROMETHEUS_ADDRESS"); v != "" {
		c.Metrics.PrometheusAddress = v
	}
	if v := os.Getenv("NAKAMA_VOICE_METRICS_HEALTH_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid METRICS_HEALTH_PORT: %w", err)
		}
		c.Metrics.HealthCheckPort = n
	}
	if v := os.Getenv("NAKAMA_VOICE_SECURITY_ADMIN_IDS"); v != "" {
		c.Security.AdminUserIDs = strings.Split(v, ",")
	}
	if v := os.Getenv("NAKAMA_VOICE_TURN_ENABLED"); v != "" {
		c.TURN.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("NAKAMA_VOICE_TURN_LISTEN_ADDRESS"); v != "" {
		c.TURN.ListenAddress = v
	}
	if v := os.Getenv("NAKAMA_VOICE_TURN_REALM"); v != "" {
		c.TURN.Realm = v
	}
	if v := os.Getenv("NAKAMA_VOICE_TURN_CREDENTIAL_TTL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid TURN_CREDENTIAL_TTL: %w", err)
		}
		c.TURN.CredentialTTL = n
	}
	if v := os.Getenv("NAKAMA_VOICE_TURN_MAX_ALLOCATIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid TURN_MAX_ALLOCATIONS: %w", err)
		}
		c.TURN.MaxAllocations = n
	}
	return nil
}

func (c *VoiceConfig) applyStorageOverrides(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) error {
	if nk == nil {
		return nil
	}
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: "voice_config", Key: "global", UserID: ""},
	})
	if err != nil {
		return fmt.Errorf("storage read: %w", err)
	}
	if len(objs) == 0 {
		return nil
	}

	overrides := make(map[string]interface{})
	if err := json.Unmarshal([]byte(objs[0].Value), &overrides); err != nil {
		return fmt.Errorf("unmarshal storage config: %w", err)
	}

	data, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("marshal overrides: %w", err)
	}

	if err := json.Unmarshal(data, c); err != nil {
		return fmt.Errorf("apply overrides: %w", err)
	}

	logger.Info("config: applied storage overrides from voice_config/global")
	return nil
}