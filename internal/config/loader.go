package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aeza/ssh-arena/internal/actions"
	"github.com/aeza/ssh-arena/internal/events"
)

type Loader struct {
	Actions *actions.Registry
	Events  *events.Registry
}

func (l Loader) LoadActions(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read action file %s: %w", path, err)
		}

		var def actions.Definition
		if err := json.Unmarshal(raw, &def); err != nil {
			return fmt.Errorf("decode action file %s: %w", path, err)
		}

		l.Actions.RegisterDefinition(def)
		return nil
	})
}

func (l Loader) LoadEvents(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read event file %s: %w", path, err)
		}

		var def events.Definition
		if err := json.Unmarshal(raw, &def); err != nil {
			return fmt.Errorf("decode event file %s: %w", path, err)
		}

		l.Events.RegisterDefinition(def)
		return nil
	})
}
