// Package main is the Nakama plugin entry point for the voice chat module.
// It constructs all subsystems in dependency order, registers RPCs and the
// match handler, starts background workers, and arranges graceful shutdown.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/heroiclabs/nakama-common/runtime"

	"github.com/aurix-voice/nakama-voice/cluster"
	voiceConfig "github.com/aurix-voice/nakama-voice/config"
	"github.com/aurix-voice/nakama-voice/metrics"
	"github.com/aurix-voice/nakama-voice/moderation"
	voiceNakama "github.com/aurix-voice/nakama-voice/nakama"
	"github.com/aurix-voice/nakama-voice/room"
	"github.com/aurix-voice/nakama-voice/sfu"
	"github.com/aurix-voice/nakama-voice/signaling"
	voiceStorage "github.com/aurix-voice/nakama-voice/storage"
	"github.com/aurix-voice/nakama-voice/turn"
)

// InitModule is the Nakama plugin entry point. It is called once when the
// server starts up and loads this module. All subsystem construction,
// RPC registration, and background worker startup happens here.
func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	logger.Info("voice module: initialising...")

	// --- Step 1: Load Configuration ---
	cfg, err := voiceConfig.Load(ctx, logger, nk)
	if err != nil {
		return fmt.Errorf("voice module: load config: %w", err)
	}
	logger.Info("voice module: configuration loaded")

	// --- Step 2: Initialise Metrics ---
	voiceMetrics := metrics.NewVoiceMetrics()
	logger.Info("voice module: prometheus metrics registered")

	// --- Step 3: Start Health Checker ---
	healthChecker := metrics.NewHealthChecker(cfg.Metrics.HealthCheckPort)
	if startErr := healthChecker.Start(ctx); startErr != nil {
		logger.Warn("voice module: health checker start failed: %v", startErr)
	} else {
		logger.Info("voice module: health checker started on port %d", cfg.Metrics.HealthCheckPort)
	}

	// --- Step 4: Create SFU Engine ---
	sfuEngine, err := sfu.NewEngine(ctx, cfg, voiceMetrics)
	if err != nil {
		return fmt.Errorf("voice module: create sfu engine: %w", err)
	}
	healthChecker.RegisterCheck("sfu", func() error {
		if sfuEngine.PeerCount() < 0 {
			return fmt.Errorf("sfu engine unhealthy")
		}
		return nil
	})
	logger.Info("voice module: SFU engine created")

	// --- Step 5: Create Room Manager ---
	roomMgr := room.NewManager(ctx, cfg, voiceMetrics)
	customCollector := metrics.NewCustomCollector(roomMgr)
	_ = customCollector // Registered via init in NewCustomCollector
	logger.Info("voice module: room manager created")

	// --- Step 6: Create Cluster Coordinator ---
	clusterCfg := cfg.GetCluster()
	coordinator := cluster.NewCoordinator(ctx, clusterCfg, voiceMetrics)
	if coordinator.IsEnabled() {
		coordinator.Start()
		healthChecker.RegisterCheck("cluster", coordinator.HealthCheck)
		logger.Info("voice module: cluster coordinator started, node=%s", coordinator.NodeID())

		// Set up state sync
		stateSync := cluster.NewStateSync(ctx, coordinator.RedisClient(), coordinator.NodeID())
		if stateSync != nil {
			if ssErr := stateSync.Start(); ssErr != nil {
				logger.Warn("voice module: state sync start failed: %v", ssErr)
			}
		}
	} else {
		logger.Info("voice module: running in single-node mode (no Redis configured)")
	}

	// --- Step 7: Create TURN Server ---
	turnCfg := cfg.GetTURN()
	var turnServer *turn.Server
	if turnCfg.Enabled {
		sharedSecret := os.Getenv("NAKAMA_VOICE_TURN_SECRET")
		if sharedSecret == "" {
			sharedSecret = "nakama-voice-turn-secret" // Fallback; override in production
		}
		turnServer = turn.NewServer(ctx, turnCfg, sharedSecret, voiceMetrics)
		if startErr := turnServer.Start(); startErr != nil {
			logger.Warn("voice module: TURN server start failed: %v", startErr)
		} else {
			healthChecker.RegisterCheck("turn", turnServer.HealthCheck)
			logger.Info("voice module: TURN server started on %s", turnCfg.ListenAddress)
		}
	}

	// --- Step 8: Create Recording Store ---
	storageCfg := cfg.Storage
	recStore, err := voiceStorage.NewRecordingStore(ctx, storageCfg.RecordingBackend, storageCfg.S3Bucket, storageCfg.S3Region, storageCfg.LocalPath)
	if err != nil {
		logger.Warn("voice module: recording store creation failed: %v", err)
		recStore, _ = voiceStorage.NewLocalStore(storageCfg.LocalPath)
	}
	logger.Info("voice module: recording store created (backend=%s)", recStore.Name())

	// --- Step 9: Create Moderation Components ---
	auditLog := moderation.NewAuditLog()
	moderator := moderation.NewModerator(auditLog, voiceMetrics)
	logger.Info("voice module: moderation system created")

	// --- Step 10: Create Nakama Integration Components ---
	storageHelper := voiceNakama.NewStorageHelper()
	notifier := voiceNakama.NewNotifier()
	presenceMgr := voiceNakama.NewPresenceManager()
	_ = presenceMgr // Used by match handler
	sessionStore := voiceStorage.NewSessionStore()

	// --- Step 11: Create Signaling Handler ---
	// Note: Signaling is handled directly in the match handler's processMessage().
	// The standalone handler is available for future use outside match context.
	_ = signaling.NewHandler(voiceMetrics)

	// --- Step 12: Create RPC Handlers ---
	rpcHandlers := voiceNakama.NewRPCHandlers(
		roomMgr, sfuEngine, cfg, voiceMetrics, moderator,
		turnServer, coordinator, storageHelper, recStore, notifier,
	)

	// --- Step 13: Register RPCs ---
	if err := rpcHandlers.RegisterAll(initializer); err != nil {
		return fmt.Errorf("voice module: register rpcs: %w", err)
	}
	logger.Info("voice module: 13 RPCs registered")

	// --- Step 14: Register Match Handler ---
	if err := initializer.RegisterMatch("voice", func() (runtime.Match, error) {
		return &voiceNakama.VoiceMatchHandler{}, nil
	}); err != nil {
		return fmt.Errorf("voice module: register match handler: %w", err)
	}
	logger.Info("voice module: match handler registered")

	// --- Step 15: Register Hooks ---
	hooks := voiceNakama.NewHooks(sessionStore)
	if err := hooks.RegisterHooks(initializer); err != nil {
		logger.Warn("voice module: hook registration failed: %v", err)
	}
	logger.Info("voice module: hooks registered")

	// --- Step 16: Graceful Shutdown ---
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("voice module: panic in shutdown goroutine: %v", r)
			}
		}()
		<-ctx.Done()
		logger.Info("voice module: shutting down...")

		if closeErr := sfuEngine.Close(); closeErr != nil {
			logger.Warn("voice module: sfu engine close: %v", closeErr)
		}
		roomMgr.Close()
		if coordinator.IsEnabled() {
			_ = coordinator.Close()
		}
		if turnServer != nil {
			_ = turnServer.Stop()
		}
		_ = healthChecker.Stop()

		logger.Info("voice module: shutdown complete")
	}()

	logger.Info("voice module: initialisation complete")
	return nil
}
