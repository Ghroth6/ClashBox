package compat

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/metacubex/mihomo/component/updater"
	C "github.com/metacubex/mihomo/constant"
)

var geoUpdateGate = make(chan struct{}, 1)

// GeoDataPath rejects arbitrary destinations before invoking an updater. The
// upstream updater owns validation, persistence and the associated reload/cache
// invalidation. Its selected asset path must agree with the wrapper request.
func GeoDataPath(kind, name string) (string, error) {
	var canonical string
	switch kind {
	case "MMDB":
		canonical = C.Path.MMDB()
	case "ASN":
		canonical = C.Path.ASN()
	case "GeoIp":
		canonical = C.Path.GeoIP()
	case "GeoSite":
		canonical = C.Path.GeoSite()
	default:
		return "", fmt.Errorf("unsupported geodata type %q", kind)
	}
	if name == "" || canonical == "" {
		return "", fmt.Errorf("geodata requires a filename and an existing home directory")
	}
	wanted, err := filepath.Abs(C.Path.Resolve(name))
	if err != nil {
		return "", err
	}
	expected, err := filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	if wanted != expected {
		return "", fmt.Errorf("%s update requires the active asset path %q; requested %q", kind, canonical, name)
	}
	return canonical, nil
}

func UpdateGeoData(kind, name string) error {
	return UpdateGeoDataContext(context.Background(), kind, name)
}

func UpdateGeoDataContext(ctx context.Context, kind, name string) error {
	select {
	case geoUpdateGate <- struct{}{}:
		defer func() { <-geoUpdateGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := GeoDataPath(kind, name); err != nil {
		return err
	}
	switch kind {
	case "MMDB":
		return updater.UpdateMMDBContext(ctx)
	case "ASN":
		return updater.UpdateASNContext(ctx)
	case "GeoIp":
		return updater.UpdateGeoIpContext(ctx)
	case "GeoSite":
		return updater.UpdateGeoSiteContext(ctx)
	}
	return fmt.Errorf("unsupported geodata type %q", kind)
}
