package configtest

import "testing"

func TestLoad(t *testing.T) {
	cfg := Load(t, map[string]string{"INGEST_ADDR": "127.0.0.1:1"})
	if cfg.IngestAddr != "127.0.0.1:1" {
		t.Errorf("cfg = %+v", cfg)
	}
}
