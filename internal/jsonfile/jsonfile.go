package jsonfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Read loads a JSON file and tolerates a UTF-8 BOM, which is common on Windows.
func Read(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := Decode(raw, target); err != nil {
		return fmt.Errorf("decode json file %s: %w", path, err)
	}
	return nil
}

// Decode unmarshals JSON after trimming a leading UTF-8 BOM and whitespace.
func Decode(raw []byte, target any) error {
	raw = bytes.TrimPrefix(raw, utf8BOM)
	raw = bytes.TrimSpace(raw)
	return json.Unmarshal(raw, target)
}
