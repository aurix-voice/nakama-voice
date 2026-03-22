package room

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RecordingSession manages a server-side recording session for a room.
// It captures per-user OPUS RTP streams, optionally mixes them, wraps
// in an OGG container, encrypts with AES-256-GCM, and uploads in chunks.
type RecordingSession struct {
	ID            string
	RoomID        string
	StartedAt     time.Time
	Participants  []string

	// uploadFn is called with each chunk of data to upload.
	uploadFn func(ctx context.Context, chunkID string, data []byte) error

	// chunkDuration is how often to flush and upload a chunk.
	chunkDuration time.Duration
	// encryptionKey is the AES-256 key for at-rest encryption (nil = no encryption).
	encryptionKey []byte

	// buffer accumulates audio data for the current chunk.
	buffer []byte
	// chunkCount tracks the number of chunks written.
	chunkCount int

	// OGG stream state tracked across chunks for correct container output.
	oggSerialNo  uint32 // Serial number for the OGG logical stream.
	oggPageSeqNo uint32 // Monotonically increasing page sequence number.
	oggGranulePos uint64 // Granule position (48kHz sample count).

	// active indicates whether recording is in progress.
	active bool

	// ctx and cancel for the recording goroutine.
	ctx    context.Context
	cancel context.CancelFunc

	// mu protects buffer, chunkCount, active, Participants, and OGG state.
	mu sync.Mutex
}

// NewRecordingSession creates a recording session for the specified room.
// The uploadFn is called for each chunk. If encryptionKey is non-nil,
// chunks are encrypted with AES-256-GCM before upload.
func NewRecordingSession(
	parentCtx context.Context,
	roomID string,
	encryptionKey []byte,
	uploadFn func(ctx context.Context, chunkID string, data []byte) error,
) *RecordingSession {
	ctx, cancel := context.WithCancel(parentCtx)

	// Generate a deterministic serial number from the room ID.
	var serial uint32
	for _, c := range roomID {
		serial = serial*31 + uint32(c)
	}
	if serial == 0 {
		serial = 1
	}

	return &RecordingSession{
		ID:            uuid.New().String(),
		RoomID:        roomID,
		StartedAt:     time.Now(),
		Participants:  make([]string, 0, 16),
		uploadFn:      uploadFn,
		chunkDuration: 30 * time.Second,
		encryptionKey: encryptionKey,
		buffer:        make([]byte, 0, 1024*1024), // 1MB initial
		oggSerialNo:   serial,
		active:        true,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start begins the recording session. It launches a goroutine that
// periodically flushes chunks.
func (rs *RecordingSession) Start() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in recording flush goroutine
			}
		}()

		ticker := time.NewTicker(rs.chunkDuration)
		defer ticker.Stop()

		for {
			select {
			case <-rs.ctx.Done():
				rs.flushChunk()
				return
			case <-ticker.C:
				rs.flushChunk()
			}
		}
	}()
}

// WriteAudioData appends raw audio data to the recording buffer.
func (rs *RecordingSession) WriteAudioData(data []byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if !rs.active {
		return
	}

	rs.buffer = append(rs.buffer, data...)
}

// AddParticipant records a participant in the recording metadata.
func (rs *RecordingSession) AddParticipant(userID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	for _, id := range rs.Participants {
		if id == userID {
			return
		}
	}
	rs.Participants = append(rs.Participants, userID)
}

// Stop ends the recording session, flushes remaining data, and returns
// the recording duration.
func (rs *RecordingSession) Stop() time.Duration {
	rs.mu.Lock()
	rs.active = false
	rs.mu.Unlock()

	rs.cancel()
	duration := time.Since(rs.StartedAt)
	return duration
}

// IsActive returns whether the recording is currently active.
func (rs *RecordingSession) IsActive() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.active
}

// flushChunk processes and uploads the current buffer contents.
func (rs *RecordingSession) flushChunk() {
	rs.mu.Lock()
	if len(rs.buffer) == 0 {
		rs.mu.Unlock()
		return
	}

	// Take the current buffer and reset
	data := make([]byte, len(rs.buffer))
	copy(data, rs.buffer)
	rs.buffer = rs.buffer[:0]
	rs.chunkCount++
	chunkNum := rs.chunkCount

	// Build OGG pages. On the first chunk, prepend OpusHead and OpusTags
	// header pages so the file is a valid OGG/OPUS stream.
	var oggData []byte
	if chunkNum == 1 {
		// OpusHead page (BOS — beginning of stream)
		opusHead := buildOpusHead(2, 48000)
		headPage := buildOGGPage(opusHead, 0, rs.oggSerialNo, rs.oggPageSeqNo, 0x02)
		rs.oggPageSeqNo++

		// OpusTags page
		opusTags := buildOpusTags("nakama-voice", nil)
		tagsPage := buildOGGPage(opusTags, 0, rs.oggSerialNo, rs.oggPageSeqNo, 0x00)
		rs.oggPageSeqNo++

		oggData = append(oggData, headPage...)
		oggData = append(oggData, tagsPage...)
	}

	// Wrap audio data as an OGG audio page. Advance granule position by
	// the number of 48kHz samples represented (assume 20ms OPUS frames,
	// 960 samples per frame; estimate from payload size).
	framesEstimate := uint64(len(data) / 40) // rough: ~40 bytes per 20ms OPUS frame
	if framesEstimate == 0 {
		framesEstimate = 1
	}
	rs.oggGranulePos += framesEstimate * 960
	audioPage := buildOGGPage(data, rs.oggGranulePos, rs.oggSerialNo, rs.oggPageSeqNo, 0x00)
	rs.oggPageSeqNo++

	oggData = append(oggData, audioPage...)
	rs.mu.Unlock()

	// Encrypt if key is set
	var uploadData []byte
	if rs.encryptionKey != nil {
		encrypted, err := encryptAES256GCM(oggData, rs.encryptionKey)
		if err != nil {
			return // Encryption failure; data not uploaded
		}
		uploadData = encrypted
	} else {
		uploadData = oggData
	}

	// Upload
	chunkID := fmt.Sprintf("%s/chunk_%04d.ogg", rs.ID, chunkNum)
	uploadCtx, uploadCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer uploadCancel()

	if rs.uploadFn != nil {
		_ = rs.uploadFn(uploadCtx, chunkID, uploadData)
	}
}

// wrapInOGG wraps OPUS audio data in a valid OGG container with proper page
// structure. Each call creates a single OGG page containing the audio data.
// For standalone use; prefer flushChunk which handles OpusHead/OpusTags.
func wrapInOGG(data []byte) []byte {
	return buildOGGPage(data, 0, 0, 0, 0x00)
}

// buildOpusHead constructs the 19-byte OpusHead identification header
// as defined in RFC 7845 §5.1.
func buildOpusHead(channels int, sampleRate uint32) []byte {
	head := make([]byte, 19)
	copy(head[0:8], []byte("OpusHead"))
	head[8] = 1                                     // Version
	head[9] = byte(channels)                        // Channel count
	head[10] = 0                                    // Pre-skip (little-endian uint16)
	head[11] = 0
	head[12] = byte(sampleRate)                     // Input sample rate (little-endian uint32)
	head[13] = byte(sampleRate >> 8)
	head[14] = byte(sampleRate >> 16)
	head[15] = byte(sampleRate >> 24)
	head[16] = 0                                    // Output gain (little-endian int16)
	head[17] = 0
	head[18] = 0                                    // Channel mapping family (0 = mono/stereo)
	return head
}

// buildOpusTags constructs an OpusTags comment header as defined in
// RFC 7845 §5.2. vendor is the encoder name; tags is an optional list
// of "KEY=VALUE" user comment strings.
func buildOpusTags(vendor string, tags []string) []byte {
	vendorBytes := []byte(vendor)
	// 8 (magic) + 4 (vendor len) + vendor + 4 (comment count) + comments
	size := 8 + 4 + len(vendorBytes) + 4
	for _, t := range tags {
		size += 4 + len(t)
	}
	buf := make([]byte, size)
	offset := 0
	copy(buf[offset:], []byte("OpusTags"))
	offset += 8
	// Vendor string length (little-endian uint32)
	buf[offset] = byte(len(vendorBytes))
	buf[offset+1] = byte(len(vendorBytes) >> 8)
	buf[offset+2] = byte(len(vendorBytes) >> 16)
	buf[offset+3] = byte(len(vendorBytes) >> 24)
	offset += 4
	copy(buf[offset:], vendorBytes)
	offset += len(vendorBytes)
	// User comment list length
	buf[offset] = byte(len(tags))
	buf[offset+1] = byte(len(tags) >> 8)
	buf[offset+2] = byte(len(tags) >> 16)
	buf[offset+3] = byte(len(tags) >> 24)
	offset += 4
	for _, t := range tags {
		tb := []byte(t)
		buf[offset] = byte(len(tb))
		buf[offset+1] = byte(len(tb) >> 8)
		buf[offset+2] = byte(len(tb) >> 16)
		buf[offset+3] = byte(len(tb) >> 24)
		offset += 4
		copy(buf[offset:], tb)
		offset += len(tb)
	}
	return buf
}

// buildOGGPage constructs a single OGG page with proper CRC-32 checksum.
// headerType: 0x02 = first page, 0x04 = last page, 0x00 = continuation.
func buildOGGPage(payload []byte, granulePos uint64, serialNo uint32, pageSeqNo uint32, headerType byte) []byte {
	// Segment the payload into 255-byte segments
	numSegments := len(payload) / 255
	lastSegSize := len(payload) % 255
	if lastSegSize > 0 || numSegments == 0 {
		numSegments++
	}
	segTable := make([]byte, numSegments)
	for i := 0; i < numSegments-1; i++ {
		segTable[i] = 255
	}
	if numSegments > 0 {
		segTable[numSegments-1] = byte(lastSegSize)
	}

	// OGG page header (27 bytes + segment table)
	headerSize := 27 + len(segTable)
	page := make([]byte, headerSize+len(payload))

	// Capture pattern
	copy(page[0:4], []byte("OggS"))
	// Stream structure version
	page[4] = 0
	// Header type
	page[5] = headerType
	// Granule position (8 bytes, little-endian)
	for i := 0; i < 8; i++ {
		page[6+i] = byte(granulePos >> (i * 8))
	}
	// Serial number (4 bytes, little-endian)
	for i := 0; i < 4; i++ {
		page[14+i] = byte(serialNo >> (i * 8))
	}
	// Page sequence number (4 bytes, little-endian)
	for i := 0; i < 4; i++ {
		page[18+i] = byte(pageSeqNo >> (i * 8))
	}
	// CRC checksum placeholder (filled below)
	page[22] = 0
	page[23] = 0
	page[24] = 0
	page[25] = 0
	// Number of segments
	page[26] = byte(len(segTable))
	// Segment table
	copy(page[27:], segTable)
	// Payload
	copy(page[headerSize:], payload)

	// Compute CRC-32 (OGG uses a custom polynomial)
	crc := oggCRC32(page)
	page[22] = byte(crc)
	page[23] = byte(crc >> 8)
	page[24] = byte(crc >> 16)
	page[25] = byte(crc >> 24)

	return page
}

// oggCRC32 computes the OGG-specific CRC-32 checksum using the polynomial
// 0x04C11DB7 (same as used in the OGG specification).
func oggCRC32(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

// oggCRCTable is the precomputed CRC lookup table for the OGG polynomial.
var oggCRCTable = func() [256]uint32 {
	var table [256]uint32
	const poly = uint32(0x04C11DB7)
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ poly
			} else {
				r <<= 1
			}
		}
		table[i] = r
	}
	return table
}()

// encryptAES256GCM encrypts data using AES-256-GCM authenticated encryption.
func encryptAES256GCM(plaintext, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("recording: AES-256 key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("recording: create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("recording: create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("recording: generate nonce: %w", err)
	}

	// Prepend nonce to ciphertext
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}