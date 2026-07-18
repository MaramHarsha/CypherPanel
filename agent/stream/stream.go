package stream

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/cypherpanel/agent/driver/docker"
)

// Streamer connects to the Docker engine to stream logs of running applications
// to JetStream, so the control plane can relay them.
type Streamer struct {
	nc     *nats.Conn
	client docker.Client

	mu      sync.Mutex
	cancel  map[string]context.CancelFunc // container_id -> cancel
}

func NewStreamer(nc *nats.Conn, client docker.Client) *Streamer {
	return &Streamer{
		nc:     nc,
		client: client,
		cancel: make(map[string]context.CancelFunc),
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
	s.cancel[containerID] = cancel

	go s.stream(ctx, appID, containerID)
}

// Stop stops streaming logs for the container.
func (s *Streamer) Stop(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cancel, exists := s.cancel[containerID]; exists {
		cancel()
		delete(s.cancel, containerID)
	}
}

// StopAll stops all active log streams.
func (s *Streamer) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, cancel := range s.cancel {
		cancel()
		delete(s.cancel, id)
	}
}

func (s *Streamer) stream(ctx context.Context, appID, containerID string) {
	pr, pw := io.Pipe()
	
	go func() {
		err := s.client.StreamLogs(ctx, containerID, pw)
		pw.CloseWithError(err)
	}()

	subject := fmt.Sprintf("logs.runtime.%s", appID)

	// Demux Docker multiplexed stream (8 byte header)
	header := make([]byte, 8)
	for {
		if ctx.Err() != nil {
			break
		}
		
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
				s.nc.Publish(subject, []byte(line))
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
	for id, cancel := range s.cancel {
		if !active[id] {
			cancel()
			delete(s.cancel, id)
		}
	}
}
