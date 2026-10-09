package collector

import (
	"fmt"
	"os"
	"time"
)

// Recolectar arma un Reporte completo. Nunca falla entero: cada sección que
// no se pueda leer se anota en Reporte.Errores y el resto sigue.
func Recolectar(agenteVersion string) Reporte {
	r := Reporte{
		Version:       VersionContrato,
		AgenteVersion: agenteVersion,
		TomadoEn:      time.Now().UTC(),
		Discos:        []Disco{},
		Volumenes:     []Volumen{},
		Red:           []Adaptador{},
		Impresoras:    []Impresora{},
		Errores:       []string{},
	}
	hostname, _ := os.Hostname()
	r.Equipo.Hostname = hostname

	paso := func(seccion string, f func() error) {
		defer func() {
			// Un pánico en una lectura (WMI raro, driver roto) no tumba el agente.
			if p := recover(); p != nil {
				r.Errores = append(r.Errores, fmt.Sprintf("%s: pánico: %v", seccion, p))
			}
		}()
		if err := f(); err != nil {
			r.Errores = append(r.Errores, fmt.Sprintf("%s: %v", seccion, err))
		}
	}

	paso("equipo", func() error { return leerEquipo(&r.Equipo) })
	paso("sistema", func() error { return leerSistema(&r.Sistema) })
	paso("cpu", func() error { return leerCPU(&r.CPU) })
	paso("memoria", func() error { return leerMemoria(&r.Memoria) })
	paso("discos", func() error { var err error; r.Discos, err = leerDiscos(); return err })
	paso("volumenes", func() error { var err error; r.Volumenes, err = leerVolumenes(); return err })
	paso("bateria", func() error { var err error; r.Bateria, err = leerBateria(); return err })
	paso("red", func() error { var err error; r.Red, err = leerRed(); return err })
	paso("impresoras", func() error { var err error; r.Impresoras, err = leerImpresoras(); return err })
	paso("seguridad", func() error { return leerSeguridad(&r.Seguridad) })

	// Con batería es laptop aunque el gabinete diga otra cosa.
	if r.Bateria != nil && r.Equipo.Tipo != "LAPTOP" {
		r.Equipo.Tipo = "LAPTOP"
	}
	return r
}
