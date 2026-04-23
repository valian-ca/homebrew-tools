// Package state persists frn's last selection (flavor, device, vmservice
// path) so the next run can reuse it.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// State mirrors .dart_tool/valian/frn.json.
//
// HasFlavor distinguishes "no flavor saved yet" from "saved, explicit empty"
// so that --no-flavor persists across runs instead of falling back to the
// default.
type State struct {
	Flavor         string `json:"flavor"`
	DeviceID       string `json:"device_id"`
	DeviceLabel    string `json:"device_label"`
	VMServiceFile  string `json:"vmservice_file"`
	HasFlavor      bool   `json:"-"`
}

// Load reads the state file at path. A missing file returns a zero State and
// no error — callers treat that as "no prior state".
func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{}, nil
		}
		return State{}, err
	}
	// Unmarshal into a map first so we can tell whether "flavor" was present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return State{}, err
	}
	var s State
	if v, ok := raw["flavor"]; ok {
		s.HasFlavor = true
		_ = json.Unmarshal(v, &s.Flavor)
	}
	if v, ok := raw["device_id"]; ok {
		_ = json.Unmarshal(v, &s.DeviceID)
	}
	if v, ok := raw["device_label"]; ok {
		_ = json.Unmarshal(v, &s.DeviceLabel)
	}
	if v, ok := raw["vmservice_file"]; ok {
		_ = json.Unmarshal(v, &s.VMServiceFile)
	}
	return s, nil
}

// Save writes state to path, creating parent directories as needed.
// Errors are returned but callers typically log-and-continue — a failed
// save shouldn't block launching Flutter.
func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{
		"flavor":          s.Flavor,
		"device_id":       s.DeviceID,
		"device_label":    s.DeviceLabel,
		"vmservice_file":  s.VMServiceFile,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
