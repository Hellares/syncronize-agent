// Package config guarda los vínculos del agente en
// %ProgramData%\Syncronize\Agent, con permisos solo para SYSTEM y
// Administradores (los tokens no los puede leer un usuario común).
//
// Un equipo puede estar vinculado con VARIOS servicios técnicos a la vez
// (el taller A, el taller B...): un vínculo por empresa, cada uno con su token, su
// consentimiento y su modo. Ninguno ve los datos ni el nombre de los otros.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	ServidorProd = "https://saas.syncronize.net.pe/api"
	ServidorBeta = "https://saas-beta.syncronize.net.pe/api"

	IntervaloPorDefecto = 15 * 60 // segundos
	intervaloMinimo     = 60
)

// Vinculo con UN servicio técnico.
type Vinculo struct {
	Servidor string `json:"servidor"`
	AgenteID string `json:"agenteId"`
	Token    string `json:"token"`
	// Nombre del servicio técnico ("Taller A"), para mostrárselo al cliente.
	Empresa           string    `json:"empresa,omitempty"`
	VinculadoEn       time.Time `json:"vinculadoEn,omitempty"`
	IntervaloSegundos int       `json:"intervaloSegundos"`
	// true = reporta cada intervalo. false = solo cuando se lo piden desde el
	// panel. Lo decide el backend (respuesta de /agentes/pedido).
	Continuo bool `json:"continuo,omitempty"`
}

type Config struct {
	Vinculos []*Vinculo `json:"vinculos"`
}

// formatoViejo: v0.2-v0.4 guardaban UN vínculo en la raíz del archivo.
type formatoViejo struct {
	Servidor          string `json:"servidor"`
	AgenteID          string `json:"agenteId"`
	Token             string `json:"token"`
	IntervaloSegundos int    `json:"intervaloSegundos"`
	Continuo          bool   `json:"continuo"`
	Revocado          bool   `json:"revocado"`
}

var ErrSinVincular = errors.New("el agente no está vinculado: corre `syncronize-agent enroll`")

// Dir es la carpeta de datos del agente.
func Dir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "Syncronize", "Agent")
}

func ruta() string { return filepath.Join(Dir(), "config.json") }

// DirColaRaiz es donde v0.2-v0.4 dejaban la cola (un solo vínculo).
func DirColaRaiz() string { return filepath.Join(Dir(), "cola") }

// DirCola guarda los reportes de UN vínculo que no se pudieron enviar.
func DirCola(agenteID string) string { return filepath.Join(DirColaRaiz(), agenteID) }

// Cargar devuelve la config. Sin vínculos = ErrSinVincular (con la config
// vacía igual, para poder agregarle uno).
func Cargar() (*Config, error) {
	b, err := os.ReadFile(ruta())
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, ErrSinVincular
	}
	if err != nil {
		return nil, err
	}
	c, err := Decodificar(b)
	if err != nil {
		return nil, fmt.Errorf("config dañada (%s): %w", ruta(), err)
	}
	if len(c.Vinculos) == 0 {
		return c, ErrSinVincular
	}
	return c, nil
}

// Decodificar lee el formato actual o el viejo (un vínculo en la raíz).
func Decodificar(b []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Vinculos == nil {
		var v formatoViejo
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		if v.Token != "" && v.Servidor != "" && !v.Revocado {
			c.Vinculos = []*Vinculo{{
				Servidor: v.Servidor, AgenteID: v.AgenteID, Token: v.Token,
				IntervaloSegundos: v.IntervaloSegundos, Continuo: v.Continuo,
			}}
		}
	}
	// Vínculos rotos (a mano, o un formato futuro) no se usan.
	validos := c.Vinculos[:0]
	for _, v := range c.Vinculos {
		if v != nil && v.Token != "" && v.Servidor != "" {
			validos = append(validos, v)
		}
	}
	c.Vinculos = validos
	return &c, nil
}

// Agregar suma un vínculo. Volver a vincular con la MISMA empresa (el backend
// devuelve el mismo agenteId) reemplaza el anterior en vez de duplicarlo.
func (c *Config) Agregar(v *Vinculo) {
	for i, x := range c.Vinculos {
		if x.AgenteID == v.AgenteID && strings.EqualFold(strings.TrimRight(x.Servidor, "/"), strings.TrimRight(v.Servidor, "/")) {
			c.Vinculos[i] = v
			return
		}
	}
	c.Vinculos = append(c.Vinculos, v)
}

// Quitar saca un vínculo (revocado desde el panel de esa empresa).
func (c *Config) Quitar(v *Vinculo) {
	out := c.Vinculos[:0]
	for _, x := range c.Vinculos {
		if x != v {
			out = append(out, x)
		}
	}
	c.Vinculos = out
}

// Intervalo devuelve el intervalo de reporte en segundos, con piso de 1 minuto.
func (v *Vinculo) Intervalo() int {
	if v.IntervaloSegundos < intervaloMinimo {
		if v.IntervaloSegundos == 0 {
			return IntervaloPorDefecto
		}
		return intervaloMinimo
	}
	return v.IntervaloSegundos
}

// Nombre para mostrar: la empresa, o el id si un vínculo viejo no la tiene.
func (v *Vinculo) Nombre() string {
	if v.Empresa != "" {
		return v.Empresa
	}
	return "servicio técnico " + v.AgenteID
}

func Guardar(c *Config) error {
	if err := asegurarDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Escritura atómica: un corte de luz no deja el archivo a medias.
	tmp := ruta() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ruta())
}

func Borrar() error {
	err := os.RemoveAll(Dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// asegurarDir crea la carpeta y le quita la herencia de permisos:
// control total para SYSTEM (SY) y Administradores (BA), nadie más.
func asegurarDir() error {
	if err := os.MkdirAll(DirColaRaiz(), 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(Dir(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("no se pudieron fijar los permisos de %s (¿corriste como administrador?): %w", Dir(), err)
	}
	return nil
}
