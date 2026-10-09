//go:build windows

package collector

import (
	"strings"
	"testing"
)

func TestTipoDeGabinete(t *testing.T) {
	casos := map[string][]int32{"LAPTOP": {10}, "ESCRITORIO": {3}, "OTRO": {2}}
	casos["LAPTOP"] = append(casos["LAPTOP"], 9)
	for quiere, tipos := range casos {
		if got := tipoDeGabinete(tipos); got != quiere {
			t.Errorf("%v → %s, quería %s", tipos, got, quiere)
		}
	}
}

func TestSerieValida(t *testing.T) {
	for _, relleno := range []string{"Default string", "To Be Filled By O.E.M.", " 0 ", "System Serial Number"} {
		if serieValida(relleno) != "" {
			t.Errorf("%q debía quedar vacía", relleno)
		}
	}
	if serieValida(" PF3ABC12 ") != "PF3ABC12" {
		t.Error("serie real mal limpiada")
	}
}

func TestEstadoImpresora(t *testing.T) {
	if estadoImpresora(3, 9) != "LISTA" || estadoImpresora(1, 9) != "ERROR" || estadoImpresora(2, 2) != "DESCONOCIDO" {
		t.Error("estado de impresora mal mapeado")
	}
}

func TestEsPuertoDeRed(t *testing.T) {
	for p, quiere := range map[string]bool{"IP_192.168.1.20": true, "WSD-93632a1e": true, "192.168.1.20": true, "USB001": false, "COM13:": false, "LPT1:": false} {
		if esPuertoDeRed(p) != quiere {
			t.Errorf("%s → %v", p, !quiere)
		}
	}
}

// Corre el colector real contra este equipo: no debe entrar en pánico ni
// dejar errores en las secciones que cualquier Windows tiene.
func TestRecolectarEnEsteEquipo(t *testing.T) {
	if testing.Short() {
		t.Skip("lee el hardware")
	}
	r := Recolectar("test")
	if r.Equipo.Hostname == "" || r.Sistema.Nombre == "" || r.CPU.Hilos == 0 || r.Memoria.TotalBytes == 0 || len(r.Volumenes) == 0 {
		t.Fatalf("reporte incompleto: %+v", r)
	}
	for _, e := range r.Errores {
		// Los contadores del disco piden admin: sin elevar, "acceso denegado" es lo esperado.
		if strings.HasPrefix(e, "discos: contadores:") && (strings.Contains(e, "denegado") || strings.Contains(strings.ToLower(e), "access denied")) {
			continue
		}
		t.Errorf("error de lectura: %s", e)
	}
}
