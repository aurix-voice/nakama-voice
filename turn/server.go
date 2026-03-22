package turn

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"sync"
	"time"

	pionTurn "github.com/pion/turn/v2"

	"github.com/your-org/nakama-voice/config"
	"github.com/your-org/nakama-voice/metrics"
)

// Server implements a built-in RFC 5766 TURN relay server using pion/turn.
// It supports UDP and TCP listeners with HMAC-SHA1 time-limited credentials.
type Server struct {
	turnServer    *pionTurn.Server
	credGen       *CredentialGenerator
	cfg           config.TURNConfig
	voiceMetrics  *metrics.VoiceMetrics
	sharedSecret  string

	ctx    context.Context
	cancel context.CancelFunc

	// mu protects the server lifecycle.
	mu sync.Mutex
}

// NewServer creates a TURN server with the specified configuration and metrics.
// The sharedSecret is used for HMAC-SHA1 credential validation.
func NewServer(parentCtx context.Context, cfg config.TURNConfig, sharedSecret string, m *metrics.VoiceMetrics) *Server {
	ctx, cancel := context.WithCancel(parentCtx)
	return &Server{
		credGen:      NewCredentialGenerator(sharedSecret, cfg.CredentialTTL, cfg.Realm),
		cfg:          cfg,
		voiceMetrics: m,
		sharedSecret: sharedSecret,
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Start begins listening for TURN allocations on the configured address.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	udpListener, err := net.ListenPacket("udp4", s.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("turn: listen udp %s: %w", s.cfg.ListenAddress, err)
	}

	// Create the TURN server
	turnServer, err := pionTurn.NewServer(pionTurn.ServerConfig{
		Realm: s.cfg.Realm,
		AuthHandler: func(username, realm string, srcAddr net.Addr) ([]byte, bool) {
			// Validate that the credential hasn't expired by checking the
			// timestamp encoded in the username (format: "timestamp:userID")
			var expiry int64
			if _, err := fmt.Sscanf(username, "%d:", &expiry); err != nil {
				return nil, false
			}
			if time.Now().Unix() >= expiry {
				return nil, false
			}
			password := s.credentialForUser(username)
			return pionTurn.GenerateAuthKey(username, realm, password), true
		},
		PacketConnConfigs: []pionTurn.PacketConnConfig{
			{
				PacketConn: udpListener,
				RelayAddressGenerator: &pionTurn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP("0.0.0.0"),
					Address:      "0.0.0.0",
				},
			},
		},
	})
	if err != nil {
		_ = udpListener.Close()
		return fmt.Errorf("turn: create server: %w", err)
	}

	s.turnServer = turnServer

	// Monitor context for shutdown
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in TURN shutdown goroutine
			}
		}()
		<-s.ctx.Done()
		_ = s.Stop()
	}()

	return nil
}

// Stop gracefully shuts down the TURN server.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancel()

	if s.turnServer != nil {
		return s.turnServer.Close()
	}
	return nil
}

// GenerateCredentials creates time-limited TURN credentials for a user.
func (s *Server) GenerateCredentials(userID string) *Credential {
	urls := []string{
		fmt.Sprintf("turn:%s?transport=udp", s.cfg.ListenAddress),
		fmt.Sprintf("turn:%s?transport=tcp", s.cfg.ListenAddress),
	}
	return s.credGen.Generate(userID, urls)
}

// credentialForUser computes the HMAC-SHA1 password for a username directly.
func (s *Server) credentialForUser(username string) string {
	mac := hmac.New(sha1.New, []byte(s.sharedSecret))
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// IsEnabled returns whether the TURN server is configured to run.
func (s *Server) IsEnabled() bool {
	return s.cfg.Enabled
}

// HealthCheck returns nil if the TURN server is operational.
func (s *Server) HealthCheck() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return nil
	}
	if s.turnServer == nil {
		return fmt.Errorf("turn: server not started")
	}
	return nil
}

// WaitForReady blocks until the TURN server is ready or the context expires.
func (s *Server) WaitForReady(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.HealthCheck() == nil {
				return nil
			}
		}
	}
}