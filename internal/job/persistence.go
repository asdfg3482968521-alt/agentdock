package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

var jobIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validJobID(id string) bool { return jobIDPattern.MatchString(id) }
func validateJobID(id string) error {
	if !validJobID(id) {
		return errors.New("invalid job_id")
	}
	return nil
}

type persistence struct{ root string }

func newPersistence(root string) (*persistence, error) {
	root = filepath.Clean(root)
	if root == "" || root == "." || !filepath.IsAbs(root) {
		return nil, errors.New("job storage root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create job storage root: %w", err)
	}
	return &persistence{root: root}, nil
}

func (p *persistence) jobDir(id string) (string, error) {
	if err := validateJobID(id); err != nil {
		return "", err
	}
	return filepath.Join(p.root, id), nil
}

func (p *persistence) save(record Record) error {
	dir, err := p.jobDir(record.JobID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create job directory: %w", err)
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal job metadata: %w", err)
	}
	body = append(body, '\n')
	if err := atomicfile.Write(filepath.Join(dir, "metadata.json"), body, 0o600); err != nil {
		return fmt.Errorf("persist job metadata: %w", err)
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
		if !entry.IsDir() || !validJobID(entry.Name()) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(p.root, entry.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var record Record
		if json.Unmarshal(body, &record) != nil || record.JobID != entry.Name() || !validJobID(record.JobID) {
			continue
		}
		result = append(result, record)
	}
	return result, nil
}

func (p *persistence) remove(id string) error {
	dir, err := p.jobDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
