package job

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type persistedOutputEvent struct {
	Cursor    int64  `json:"cursor"`
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	Bytes     int    `json:"bytes"`
}

type outputStore struct {
	mu            sync.Mutex
	path          string
	limits        Limits
	redact        func(string) string
	now           func() time.Time
	memory        []persistedOutputEvent
	memoryBytes   int64
	nextCursor    int64
	outputBytes   int64
	diskBytes     int64
	diskAccepting bool
}

func newOutputStore(jobDir string, limits Limits, redact func(string) string, now func() time.Time) *outputStore {
	return &outputStore{
		path: filepath.Join(jobDir, "events.ndjson"), limits: limits, redact: redact, now: now, diskAccepting: true,
	}
}

func (s *outputStore) initialize(initialCursor, initialBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	s.nextCursor = initialCursor
	s.outputBytes = initialBytes
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr == nil {
		s.diskBytes = info.Size()
	}
	s.diskAccepting = s.diskBytes < s.limits.MaxDiskOutputBytesPerJob

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var recoveredBytes int64
	for scanner.Scan() {
		var event persistedOutputEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Cursor < 0 || event.Text == "" {
			continue
		}
		if event.Bytes <= 0 {
			event.Bytes = len([]byte(event.Text))
		}
		if event.Timestamp == "" {
			event.Timestamp = time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
		}
		if event.Stream == "" {
			event.Stream = "event"
		}
		if event.Cursor+1 > s.nextCursor {
			s.nextCursor = event.Cursor + 1
		}
		recoveredBytes += int64(event.Bytes)
		s.pushMemoryLocked(event)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if recoveredBytes > s.outputBytes {
		s.outputBytes = recoveredBytes
	}
	return nil
}

func (s *outputStore) cursorAndBytes() (int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextCursor, s.outputBytes
}

func (s *outputStore) append(event RuntimeOutputEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.redact(event.Text)
	if text == "" {
		return nil
	}
	maxChunk := 64 << 10
	if int64(maxChunk) > s.limits.MemoryOutputBytesPerJob {
		maxChunk = int(s.limits.MemoryOutputBytesPerJob)
	}
	if maxChunk > s.limits.MaxOutputBytesPerResponse {
		maxChunk = s.limits.MaxOutputBytesPerResponse
	}
	if maxChunk < 1 {
		maxChunk = 1
	}
	for _, piece := range splitUTF8(text, maxChunk) {
		when := event.Timestamp
		if when.IsZero() {
			when = s.now()
		}
		stream := event.Stream
		if stream == "" {
			stream = "event"
		}
		stored := persistedOutputEvent{Cursor: s.nextCursor, Stream: stream, Text: piece, Timestamp: when.UTC().Format(time.RFC3339Nano), Bytes: len([]byte(piece))}
		s.nextCursor++
		s.outputBytes += int64(stored.Bytes)
		s.pushMemoryLocked(stored)
		if err := s.appendDiskLocked(stored); err != nil {
			return err
		}
	}
	return nil
}

func (s *outputStore) pushMemoryLocked(event persistedOutputEvent) {
	s.memory = append(s.memory, event)
	s.memoryBytes += int64(event.Bytes)
	for s.memoryBytes > s.limits.MemoryOutputBytesPerJob && len(s.memory) > 0 {
		s.memoryBytes -= int64(s.memory[0].Bytes)
		s.memory = s.memory[1:]
	}
}

func (s *outputStore) appendDiskLocked(event persistedOutputEvent) error {
	if !s.diskAccepting {
		return nil
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if s.diskBytes+int64(len(body)) > s.limits.MaxDiskOutputBytesPerJob {
		s.diskAccepting = false
		return nil
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	s.diskBytes += int64(len(body))
	return nil
}

func (s *outputStore) read(cursor int64, limit int) (OutputResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor < 0 {
		return OutputResult{}, errors.New("cursor must be non-negative")
	}
	if limit <= 0 {
		limit = s.limits.MaxOutputBytesPerResponse
	}
	if limit > s.limits.MaxOutputBytesPerResponse {
		limit = s.limits.MaxOutputBytesPerResponse
	}
	if limit < 1 {
		limit = 1
	}

	all, err := s.readDiskLocked()
	if err != nil {
		return OutputResult{}, err
	}
	byCursor := make(map[int64]persistedOutputEvent, len(all)+len(s.memory))
	for _, event := range all {
		byCursor[event.Cursor] = event
	}
	for _, event := range s.memory {
		byCursor[event.Cursor] = event
	}

	result := OutputResult{Chunks: []OutputChunk{}, NextCursor: cursor}
	used := 0
	for result.NextCursor < s.nextCursor {
		event, ok := byCursor[result.NextCursor]
		if !ok {
			result.Truncated = true
			earliest := s.nextCursor
			for c := range byCursor {
				if c >= result.NextCursor && c < earliest {
					earliest = c
				}
			}
			if earliest == s.nextCursor {
				break
			}
			result.NextCursor = earliest
			event = byCursor[earliest]
		}
		if used+event.Bytes > limit {
			result.Truncated = true
			break
		}
		chunk := OutputChunk{Cursor: event.Cursor, Stream: event.Stream, Text: event.Text, Timestamp: event.Timestamp, Bytes: event.Bytes}
		result.Chunks = append(result.Chunks, chunk)
		result.Text += event.Text
		used += event.Bytes
		result.NextCursor = event.Cursor + 1
	}
	if result.NextCursor < s.nextCursor {
		result.Truncated = true
	}
	return result, nil
}

func (s *outputStore) readDiskLocked() ([]persistedOutputEvent, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	var result []persistedOutputEvent
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = []byte(strings.TrimSpace(string(line)))
			if len(line) > 0 {
				var event persistedOutputEvent
				if json.Unmarshal(line, &event) == nil && event.Cursor >= 0 && event.Text != "" {
					if event.Bytes <= 0 {
						event.Bytes = len([]byte(event.Text))
					}
					result = append(result, event)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read job output spool: %w", readErr)
		}
	}
	return result, nil
}
