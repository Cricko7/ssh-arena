package exchange

import (
	"encoding/json"
	"fmt"
	"os"
)

func LoadTickers(path string) ([]Ticker, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tickers: %w", err)
	}

	var catalog Catalog
	if err := json.Unmarshal(raw, &catalog); err == nil && len(catalog.Tickers) > 0 {
		return catalog.Tickers, nil
	}

	var tickers []Ticker
	if err := json.Unmarshal(raw, &tickers); err != nil {
		return nil, fmt.Errorf("decode tickers: %w", err)
	}
	return tickers, nil
}
