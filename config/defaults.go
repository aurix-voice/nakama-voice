// Package config provides configuration structures and loading logic for the
// voice chat module. It supports compiled defaults, environment variable
// overrides, and hot-reloadable Nakama storage overrides.
package config

// DefaultConfig returns production-safe default values for every
// configuration field. These values are used as the base layer;
// environment variables and storage overrides may supersede them.
func DefaultConfig() *VoiceConfig {
	return &VoiceConfig{
		SFU: SFUConfig{
			ListenAddress:     "0.0.0.0",
			ExternalAddress:   "",
			UDPPortMin:        10000,
			UDPPortMax:        10999,
			MaxPeerConnections: 1000,
			ICETimeout:        30,
			ICEKeepalive:      5,
			STUNServers:       []string{"stun:stun.l.google.com:19302"},
			TURNServers:       []TURNServerConfig{},
			SimulcastEnabled:  false,
			REDEnabled:        false,
			DTXEnabled:        true,
			RTCPFeedbackInterval: 1,
			MaxBandwidthKbps:  128,
		},
		Audio: AudioConfig{
			VADAggressiveness: 2,
			VADHangoverMs:     300,
			AGCTargetDBFS:     -18,
			NoiseSuppression:  1,
			SpatialEnabled:    true,
			HRTFEnabled:       true,
			MaxSpatialDistance: 100.0,
			AttenuationModel:  "logarithmic",
		},
		Room: RoomDefaults{
			MaxParticipants:    50,
			DefaultChannelType: "non_positional",
			AutoRecording:      false,
		},
		Cluster: ClusterConfig{
			RedisAddress:       "",
			RedisPassword:      "",
			RedisDB:            0,
			PoolMinConns:       10,
			PoolMaxConns:       100,
			HeartbeatIntervalS: 5,
			BridgeUDPPortMin:   11000,
			BridgeUDPPortMax:   11099,
		},
		Storage: StorageConfig{
			RecordingBackend: "local",
			S3Bucket:         "",
			S3Region:         "us-east-1",
			GCSBucket:        "",
			EncryptionEnabled: true,
			LocalPath:        "/tmp/nakama-voice-recordings",
		},
		Metrics: MetricsConfig{
			PrometheusAddress: ":9100",
			HealthCheckPort:   9101,
		},
		Security: SecurityConfig{
			AdminUserIDs:     []string{},
			BanAppealTTLDays: 30,
		},
		TURN: TURNConfig{
			Enabled:          true,
			ListenAddress:    "0.0.0.0:3478",
			TLSListenAddress: "",
			Realm:            "nakama-voice",
			CredentialTTL:    86400,
			MaxAllocations:   500,
			MaxBandwidthKbps: 256,
		},
	}
}