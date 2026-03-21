package exchange

import (
	"fmt"

	"github.com/aeza/ssh-arena/internal/jsonfile"
)

func LoadTickers(path string) ([]Ticker, error) {
	var catalog Catalog
	if err := jsonfile.Read(path, &catalog); err == nil && len(catalog.Tickers) > 0 {
		return catalog.Tickers, nil
	}

	var tickers []Ticker
	if err := jsonfile.Read(path, &tickers); err != nil {
		return nil, fmt.Errorf("decode tickers: %w", err)
	}
	return tickers, nil
}
