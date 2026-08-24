package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

type persistence struct{ root string }

func newPersistence(root string) (*persistence, error) {
	root = filepath.Clean(root)
	if root == "" || root == "." || !filepath.IsAbs(root) {
		return nil, errors.New("session storage root must be absolute")
	}
	if filepath.Dir(root) == root {
		return nil, errors.New("refusing filesystem root as session storage root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create session storage root: %w", err)
	}
	return &persistence{root: root}, nil
}

func (p *persistence) metadataPath(sessionID string) string {
	return filepath.Join(p.root, sessionID, "metadata.json")
}

func (p *persistence) save(record Record) error {
	dir := filepath.Join(p.root, record.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session metadata: %w", err)
	}
	body = append(body, '\n')
	if err := atomicfile.Write(filepath.Join(dir, "metadata.json"), body, 0o600); err != nil {
		return fmt.Errorf("persist session metadata: %w", err)
	}
	return nil
}

func (p *persistence) loadAll() ([]Record, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(p.root, entry.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var record Record
		if json.Unmarshal(body, &record) != nil || record.SessionID == "" || record.SessionID != entry.Name() {
			continue
		}
		if record.MetadataVersion != ContractVersion || !knownState(record.State) {
			continue
		}
		if record.JobIDs == nil {
			record.JobIDs = []string{}
		}
		result = append(result, record)
	}
	return result, nil
}
