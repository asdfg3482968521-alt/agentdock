package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
)

type agentPersistence struct {
	root       string
	recordsDir string
	outputDir  string

	// beforeSave is an internal test seam used to deterministically observe or
	// fail a persistence boundary. Production instances leave it nil.
	beforeSave func(Record) error
}

func newAgentPersistence(root string) (*agentPersistence, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("agent storage root must be absolute")
	}
	p := &agentPersistence{root: root, recordsDir: filepath.Join(root, "records"), outputDir: filepath.Join(root, "output")}
	for _, dir := range []string{p.root, p.recordsDir, p.outputDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := securepath.EnsurePrivate(dir); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *agentPersistence) save(record Record) error {
	if p.beforeSave != nil {
		if err := p.beforeSave(record); err != nil {
			return err
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := filepath.Join(p.recordsDir, record.AgentID+".json")
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return securepath.EnsurePrivate(path)
}

func (p *agentPersistence) loadAll() ([]Record, error) {
	entries, err := os.ReadDir(p.recordsDir)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.recordsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("parse agent record %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(record.AgentID) == "" || strings.TrimSpace(record.SessionID) == "" {
			return nil, errors.New("invalid durable agent record")
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].AgentID < records[j].AgentID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

func (p *agentPersistence) outputPath(agentID string) string {
	return filepath.Join(p.outputDir, agentID+".log")
}
