package compat

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/metacubex/mihomo/component/platformnetwork"
)

// A DNS-only or neighbour-table update cannot describe a consistent network.
// Decode and publish synchronously so an acknowledgement means acceptance.
func PublishNetworkSnapshot(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot platformnetwork.Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("network snapshot must contain exactly one JSON object")
	}
	return platformnetwork.Publish(snapshot)
}
