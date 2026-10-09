package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hellares/syncronize-agent/internal/config"
)

func TestColaRecortaLosMasViejos(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := os.MkdirAll(config.DirCola("ag1"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxCola+5; i++ {
		if err := encolar("ag1", []byte(`{}`), base.Add(time.Duration(i)*15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := listarCola("ag1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != maxCola {
		t.Fatalf("en cola %d, quería %d", len(p), maxCola)
	}
	// Se fueron los 5 primeros: el más viejo que queda es el 6.º.
	if got, want := filepath.Base(p[0]), base.Add(5*15*time.Minute).Format("20060102T150405.000000000")+".json"; got != want {
		t.Fatalf("más viejo = %s, quería %s", got, want)
	}
}
