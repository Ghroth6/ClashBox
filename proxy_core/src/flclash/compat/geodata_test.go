package compat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/component/geodata"
	C "github.com/metacubex/mihomo/constant"
)

func geoHome(t *testing.T) string {
	t.Helper()
	old := C.Path.HomeDir()
	home := t.TempDir()
	C.SetHomeDir(home)
	t.Cleanup(func() { C.SetHomeDir(old) })
	return home
}

func TestGeoDataAcceptsOnlyActiveAssetPath(t *testing.T) {
	home := geoHome(t)
	for kind, name := range map[string]string{"MMDB": "geoip.metadb", "ASN": "ASN.mmdb", "GeoIp": "GeoIP.dat", "GeoSite": "GeoSite.dat"} {
		want := filepath.Join(home, name)
		for _, request := range []string{name, want} {
			got, err := GeoDataPath(kind, request)
			if err != nil || filepath.Clean(got) != want {
				t.Fatalf("%s/%s: %s, %v", kind, request, got, err)
			}
		}
		for _, request := range []string{"", "other.dat", "../" + name, filepath.Join(t.TempDir(), name)} {
			if _, err := GeoDataPath(kind, request); err == nil {
				t.Fatalf("accepted %s/%q", kind, request)
			}
		}
	}
	if _, err := GeoDataPath("unknown", "GeoIP.dat"); err == nil {
		t.Fatal("accepted unknown asset type")
	}
	C.SetHomeDir(filepath.Join(home, "missing"))
	if _, err := GeoDataPath("MMDB", "geoip.metadb"); err == nil {
		t.Fatal("accepted unavailable home")
	}
}

func TestMMDBAliasCannotSilentlyUpdateDifferentFile(t *testing.T) {
	home := geoHome(t)
	for _, name := range []string{"Country.mmdb", "geoip.metadb"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := GeoDataPath("MMDB", "Country.mmdb"); err != nil {
		t.Fatal(err)
	}
	if _, err := GeoDataPath("MMDB", "geoip.metadb"); err == nil {
		t.Fatal("silently redirected update to Country.mmdb")
	}
}

func TestOfficialUpdatersRejectInvalidDownloadWithoutReplacingAsset(t *testing.T) {
	home := geoHome(t)
	for _, test := range []struct {
		kind, name string
		get        func() string
		set        func(string)
	}{
		{"MMDB", "geoip.metadb", geodata.MmdbUrl, geodata.SetMmdbUrl},
		{"ASN", "ASN.mmdb", geodata.ASNUrl, geodata.SetASNUrl},
		{"GeoIp", "GeoIP.dat", geodata.GeoIpUrl, geodata.SetGeoIpUrl},
		{"GeoSite", "GeoSite.dat", geodata.GeoSiteUrl, geodata.SetGeoSiteUrl},
	} {
		t.Run(test.kind, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Write([]byte("invalid downloaded geodata"))
			}))
			defer server.Close()
			oldURL := test.get()
			test.set(server.URL)
			defer test.set(oldURL)
			asset := filepath.Join(home, test.name)
			if err := os.WriteFile(asset, []byte("original asset"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := UpdateGeoData(test.kind, "other.dat"); err == nil {
				t.Fatal("arbitrary destination accepted")
			}
			if requests.Load() != 0 {
				t.Fatal("invalid path performed a download")
			}
			err := UpdateGeoData(test.kind, test.name)
			if err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("expected format validation error: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("updater requests = %d", requests.Load())
			}
			data, err := os.ReadFile(asset)
			if err != nil || string(data) != "original asset" {
				t.Fatalf("invalid download replaced asset: %q, %v", data, err)
			}
		})
	}
}
