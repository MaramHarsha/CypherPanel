package stream

import (
	"context"
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// Streamer connects to the Docker engine to stream logs of running applications
// to JetStream, so the control plane can relay them.
type Streamer struct {
	nc       *nats.Conn
	client   docker.Client
	serverID string

	mu     sync.Mutex
	cancel map[string]*streamHandle // container_id -> cancel
}

type streamHandle struct {
	cancel context.CancelFunc
}

func NewStreamer(nc *nats.Conn, client docker.Client, serverID string) *Streamer {
	return &Streamer{
		nc:       nc,
		client:   client,
		serverID: serverID,
		cancel:   make(map[string]*streamHandle),
	}
}

// Ensure starts streaming logs for the container if not already streaming.
func (s *Streamer) Ensure(appID, containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.cancel[containerID]; exists {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	handle := &streamHandle{cancel: cancel}
	s.cancel[containerID] = handle

	go s.stream(ctx, appID, containerID, handle)
}

// Stop stops streaming logs for the container.
func (s *Streamer) Stop(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if handle, exists := s.cancel[containerID]; exists {
		handle.cancel()
		delete(s.cancel, containerID)
	}
}

// StopAll stops all active log streams.
func (s *Streamer) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, handle := range s.cancel {
		handle.cancel()
		delete(s.cancel, id)
	}
}

func (s *Streamer) stream(ctx context.Context, appID, containerID string, handle *streamHandle) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cancel[containerID] == handle {
			delete(s.cancel, containerID)
		}
	}()

	pr, pw := io.Pipe()

	go func() {
		err := s.client.StreamLogs(ctx, containerID, pw)
		pw.CloseWithError(err)
	}()

	subject := subjects.RuntimeLog(s.serverID, appID)

	// Demux Docker multiplexed stream (8 byte header)
	header := make([]byte, 8)
	for ctx.Err() == nil {
		_, err := io.ReadFull(pr, header)
		if err != nil {
			break
		}

		size := binary.BigEndian.Uint32(header[4:8])
		payload := make([]byte, size)
		_, err = io.ReadFull(pr, payload)
		if err != nil {
			break
		}

		// NATS JetStream might chunk or we can just send line by line.
		// For simplicity, we just publish the chunk as is (often a single line).
		lines := strings.Split(string(payload), "\n")
		for _, line := range lines {
			line = strings.TrimSuffix(line, "\r")
			if line != "" {
				_ = s.nc.Publish(subject, []byte(line))
			}
		}
	}
}

// Start begins a background polling loop to sync streams.
func (s *Streamer) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.StopAll()
			return
		case <-ticker.C:
			s.sync(ctx)
		}
	}
}

func (s *Streamer) sync(ctx context.Context) {
	containers, err := s.client.ListManaged(ctx)
	if err != nil {
		return
	}

	active := make(map[string]bool)
	for _, c := range containers {
		if c.Running {
			active[c.ID] = true
			s.Ensure(c.AppID, c.ID)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, handle := range s.cancel {
		if !active[id] {
			handle.cancel()
			delete(s.cancel, id)
		}
	}
}
