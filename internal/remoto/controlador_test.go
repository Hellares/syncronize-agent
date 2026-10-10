package remoto

import (
	"errors"
	"io"
	"log"
	"testing"
	"time"
)

type falso struct {
	instalado, encendido, sesion bool
	errInstalar                  error
	instalaciones, apagados      int
	configurado                  []Servidor
}

func (f *falso) Instalado() bool { return f.instalado }
func (f *falso) Instalar() error {
	f.instalaciones++
	if f.errInstalar != nil {
		return f.errInstalar
	}
	f.instalado = true
	return nil
}
func (f *falso) Preparar() error { f.encendido = false; return nil }
func (f *falso) Configurar(s Servidor) error {
	f.configurado = append(f.configurado, s)
	return nil
}
func (f *falso) Encender() (string, error)   { f.encendido = true; return "123456789", nil }
func (f *falso) LeerID() (string, error)     { return "123456789", nil }
func (f *falso) Apagar() error               { f.encendido = false; f.apagados++; return nil }
func (f *falso) Encendido() bool             { return f.encendido }
func (f *falso) SesionActiva() bool          { return f.sesion }
func (f *falso) AsegurarInicioManual() error { return nil }

var sinLog = log.New(io.Discard, "", 0)

func en(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }

func TestInstalaConfiguraYEnciendeAlPedir(t *testing.T) {
	f := &falso{}
	instalo := false
	c := nuevoCon(f, false, 1, func() { instalo = true }, sinLog)
	c.Pedir("a", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())

	e, _ := c.Estado()
	if e.Estado != "ACTIVO" || e.ID != "123456789" {
		t.Fatalf("estado = %+v", e)
	}
	if !instalo || !c.propio || len(f.configurado) != 1 {
		t.Fatalf("instalo=%v propio=%v configurado=%v", instalo, c.propio, f.configurado)
	}
}

func TestApagaAlTerminar(t *testing.T) {
	f := &falso{instalado: true}
	c := nuevoCon(f, true, 1, nil, sinLog)
	c.Pedir("a", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())
	c.Pedir("a", Pedido{}) // "Terminar" desde el panel
	c.evaluar(time.Now())
	if f.encendido {
		t.Fatal("sigue encendido")
	}
	if e, _ := c.Estado(); e.Estado != "INACTIVO" {
		t.Fatalf("estado = %+v", e)
	}
}

func TestVencidoConSesionAbiertaEspera(t *testing.T) {
	f := &falso{instalado: true}
	c := nuevoCon(f, true, 1, nil, sinLog)
	c.Pedir("a", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())
	f.sesion = true
	c.Pedir("a", Pedido{Hasta: en(-time.Minute)})
	c.evaluar(time.Now())
	if !f.encendido {
		t.Fatal("cortó una sesión abierta al vencer el plazo")
	}
	f.sesion = false
	c.evaluar(time.Now())
	if f.encendido {
		t.Fatal("no apagó al terminar la sesión")
	}
}

func TestSigueEncendidoMientrasOtroLoQuiera(t *testing.T) {
	f := &falso{instalado: true}
	c := nuevoCon(f, true, 2, nil, sinLog)
	c.Pedir("a", Pedido{Hasta: en(time.Hour)})
	c.Pedir("b", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())
	c.Pedir("a", Pedido{})
	c.evaluar(time.Now())
	if !f.encendido {
		t.Fatal("lo apagó aunque el otro servicio técnico lo sigue usando")
	}
}

func TestAlArrancarNoApagaHastaOirATodos(t *testing.T) {
	// El agente se reinició con RustDesk encendido (un técnico conectado).
	f := &falso{instalado: true, encendido: true}
	c := nuevoCon(f, true, 2, nil, sinLog)
	c.Pedir("a", Pedido{})
	c.evaluar(time.Now())
	if !f.encendido {
		t.Fatal("apagó antes de oír al otro servicio técnico")
	}
	c.Pedir("b", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())
	if !f.encendido || f.apagados != 0 {
		t.Fatalf("encendido=%v apagados=%d: debía tomar el ID sin reiniciar", f.encendido, f.apagados)
	}
	if e, _ := c.Estado(); e.Estado != "ACTIVO" {
		t.Fatalf("estado = %+v", e)
	}
}

func TestRustDeskDelClienteNoSeApagaSiYaEstabaEncendido(t *testing.T) {
	f := &falso{instalado: true, encendido: true}
	c := nuevoCon(f, false, 1, nil, sinLog)
	c.Pedir("a", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())
	c.Pedir("a", Pedido{})
	c.evaluar(time.Now())
	if !f.encendido || len(f.configurado) != 0 {
		t.Fatalf("tocó el RustDesk propio del cliente: encendido=%v configurado=%v", f.encendido, f.configurado)
	}
}

func TestTrasUnErrorNoReintentaEnCadaRevision(t *testing.T) {
	f := &falso{errInstalar: errors.New("sin internet")}
	c := nuevoCon(f, false, 1, nil, sinLog)
	c.Pedir("a", Pedido{Hasta: en(time.Hour)})
	c.evaluar(time.Now())
	c.evaluar(time.Now())
	c.evaluar(time.Now())
	if f.instalaciones != 1 {
		t.Fatalf("intentó instalar %d veces", f.instalaciones)
	}
	if e, _ := c.Estado(); e.Estado != "ERROR" {
		t.Fatalf("estado = %+v", e)
	}
	// Un pedido nuevo (el técnico vuelve a tocar el botón) reintenta.
	c.Pedir("a", Pedido{Hasta: en(2 * time.Hour)})
	c.evaluar(time.Now())
	if f.instalaciones != 2 {
		t.Fatalf("no reintentó con el pedido nuevo (%d)", f.instalaciones)
	}
}

func TestElRecalculoDeSegundosNoEsUnPedidoNuevo(t *testing.T) {
	a := Pedido{Hasta: en(time.Hour)}
	b := Pedido{Hasta: en(time.Hour + 20*time.Second)}
	if distinto(a, b) {
		t.Fatal("20 s de diferencia cuentan como pedido nuevo")
	}
	if !distinto(a, Pedido{}) {
		t.Fatal("terminar no cuenta como pedido nuevo")
	}
}
