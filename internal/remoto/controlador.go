package remoto

import (
	"context"
	"log"
	"sync"
	"time"
)

// Estado de RustDesk que se le informa a cada servicio técnico autorizado.
type Estado struct {
	// INACTIVO | INSTALANDO | ACTIVO | ERROR
	Estado  string `json:"estado"`
	ID      string `json:"id,omitempty"`
	Detalle string `json:"detalle,omitempty"`
	Version string `json:"version,omitempty"`
}

// Pedido de UN servicio técnico, tal como lo manda su backend.
type Pedido struct {
	// nil = no pidió soporte (o lo terminó). En el pasado = se le venció el plazo.
	Hasta    *time.Time
	Servidor Servidor
}

// operaciones son las acciones sobre RustDesk (en los tests, falsas).
type operaciones interface {
	Instalado() bool
	Instalar() error
	Configurar(Servidor) error
	Encender() (string, error)
	LeerID() (string, error)
	Apagar() error
	Encendido() bool
	SesionActiva() bool
	AsegurarInicioManual() error
}

const (
	// Cada cuánto se revisan los plazos aunque no llegue nada nuevo.
	revision = 30 * time.Second
	// Tras un error (sin internet para bajar RustDesk...) no se reintenta en
	// cada revisión: espera esto o un pedido nuevo.
	esperaTrasError = 5 * time.Minute
	// Al arrancar el agente no se apaga nada hasta oír a todos los servicios
	// técnicos (o pasado este rato): si un técnico está conectado mientras
	// actualiza el agente, no se le corta la sesión.
	graciaInicial = 2 * time.Minute
)

// Controlador junta los pedidos de todos los servicios técnicos autorizados
// (hay UN RustDesk por equipo) y lo enciende mientras alguno lo quiera.
type Controlador struct {
	ops operaciones
	log *log.Logger
	// propio = lo instaló el agente: se configura, se apaga y se desinstala.
	// Si el equipo ya tenía su RustDesk, solo se enciende si estaba apagado
	// (y se apaga solo si lo encendió el agente).
	alInstalar func()
	esperados  int
	inicio     time.Time

	mu          sync.Mutex
	propio      bool
	arrancamos  bool
	pedidos     map[string]Pedido
	estado      Estado
	cambio      chan struct{}
	despertar   chan struct{}
	ultimoErr   time.Time
	nuevo       bool // llegó un pedido distinto desde el último error
	servidor    *Servidor
	avisoSesion bool
}

// Nuevo arma el controlador. esperados = cuántos vínculos autorizaron el
// soporte; alInstalar se llama si el agente instala RustDesk (para guardarlo).
func Nuevo(propio bool, esperados int, alInstalar func(), l *log.Logger) *Controlador {
	return nuevoCon(sistema{}, propio, esperados, alInstalar, l)
}

func nuevoCon(ops operaciones, propio bool, esperados int, alInstalar func(), l *log.Logger) *Controlador {
	if alInstalar == nil {
		alInstalar = func() {}
	}
	c := &Controlador{
		ops: ops, log: l, alInstalar: alInstalar, esperados: esperados, inicio: time.Now(),
		propio: propio, pedidos: map[string]Pedido{},
		cambio: make(chan struct{}), despertar: make(chan struct{}, 1),
	}
	c.estado = c.estadoInicial()
	return c
}

func (c *Controlador) estadoInicial() Estado {
	if !c.ops.Instalado() {
		return Estado{Estado: "INACTIVO"}
	}
	return Estado{Estado: "INACTIVO", Version: Version}
}

// Pedir registra lo que manda el backend de un servicio técnico.
func (c *Controlador) Pedir(agenteID string, p Pedido) {
	c.mu.Lock()
	antes, habia := c.pedidos[agenteID]
	c.pedidos[agenteID] = p
	if !habia || distinto(antes, p) {
		c.nuevo = true
	}
	c.mu.Unlock()
	c.Despertar()
}

// Olvidar quita a un servicio técnico (desvinculado).
func (c *Controlador) Olvidar(agenteID string) {
	c.mu.Lock()
	delete(c.pedidos, agenteID)
	if c.esperados > 0 {
		c.esperados--
	}
	c.mu.Unlock()
	c.Despertar()
}

func (c *Controlador) Despertar() {
	select {
	case c.despertar <- struct{}{}:
	default:
	}
}

// distinto ignora los segundos de diferencia: el backend manda "quedan N
// segundos" en cada pregunta y la hora calculada se mueve un poco.
func distinto(a, b Pedido) bool {
	if (a.Hasta == nil) != (b.Hasta == nil) || a.Servidor != b.Servidor {
		return true
	}
	if a.Hasta == nil {
		return false
	}
	d := a.Hasta.Sub(*b.Hasta)
	return d > time.Minute || d < -time.Minute
}

// Estado actual y un canal que se cierra cuando cambia.
func (c *Controlador) Estado() (Estado, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.estado, c.cambio
}

func (c *Controlador) fijar(e Estado) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e == c.estado {
		return
	}
	c.estado = e
	close(c.cambio)
	c.cambio = make(chan struct{})
}

// Correr revisa los pedidos hasta que ctx se cancele. Al terminar NO apaga
// RustDesk: si un técnico conectado reinicia o actualiza el agente, la sesión
// sigue; el agente nuevo decide.
func (c *Controlador) Correr(ctx context.Context) {
	if c.propio && c.ops.Instalado() {
		if err := c.ops.AsegurarInicioManual(); err != nil {
			c.log.Printf("soporte remoto: %v", err)
		}
	}
	t := time.NewTicker(revision)
	defer t.Stop()
	for {
		c.evaluar(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-c.despertar:
		case <-t.C:
		}
	}
}

func (c *Controlador) evaluar(ahora time.Time) {
	c.mu.Lock()
	deseado, vencido := false, false
	var serv Servidor
	for _, p := range c.pedidos {
		switch {
		case p.Hasta == nil:
		case p.Hasta.After(ahora):
			if !deseado {
				serv = p.Servidor
			}
			deseado = true
		default:
			vencido = true
		}
	}
	oidos := len(c.pedidos) >= c.esperados || ahora.Sub(c.inicio) > graciaInicial
	reintentar := c.nuevo || ahora.Sub(c.ultimoErr) > esperaTrasError
	propio, arrancamos := c.propio, c.arrancamos
	cambioServidor := c.servidor != nil && *c.servidor != serv
	c.mu.Unlock()

	encendido := c.ops.Instalado() && c.ops.Encendido()
	if deseado {
		actual, _ := c.Estado()
		if encendido && actual.Estado == "ACTIVO" && !(propio && cambioServidor) {
			return
		}
		if actual.Estado == "ERROR" && !reintentar {
			return
		}
		// Ya encendido (p. ej. el agente se reinició durante una sesión): se
		// toma el ID sin reiniciar RustDesk, para no cortarle al técnico.
		if encendido && !(propio && cambioServidor) {
			if id, err := c.ops.LeerID(); err == nil {
				c.fijar(Estado{Estado: "ACTIVO", ID: id, Version: Version})
				return
			}
		}
		c.encender(serv, encendido)
		return
	}

	if !encendido {
		if actual, _ := c.Estado(); actual.Estado == "ACTIVO" {
			c.fijar(Estado{Estado: "INACTIVO", Version: Version})
		}
		return
	}
	// Encendido y nadie lo quiere.
	if !oidos || (!propio && !arrancamos) {
		return
	}
	if vencido && c.ops.SesionActiva() {
		c.mu.Lock()
		if !c.avisoSesion {
			c.avisoSesion = true
			c.log.Printf("soporte remoto: venció el plazo pero hay una sesión abierta; se apaga al terminar")
		}
		c.mu.Unlock()
		return
	}
	if err := c.ops.Apagar(); err != nil {
		c.log.Printf("soporte remoto: no se pudo apagar: %v", err)
		return
	}
	c.log.Printf("soporte remoto: apagado")
	c.mu.Lock()
	c.arrancamos, c.avisoSesion = false, false
	c.mu.Unlock()
	c.fijar(Estado{Estado: "INACTIVO", Version: Version})
}

func (c *Controlador) encender(serv Servidor, encendido bool) {
	c.mu.Lock()
	c.nuevo = false
	c.mu.Unlock()
	falla := func(msg string, err error) {
		c.log.Printf("soporte remoto: %s: %v", msg, err)
		c.mu.Lock()
		c.ultimoErr = time.Now()
		c.mu.Unlock()
		c.fijar(Estado{Estado: "ERROR", Detalle: msg + ": " + err.Error(), Version: c.version()})
	}

	if !c.ops.Instalado() {
		c.log.Printf("soporte remoto: instalando RustDesk %s", Version)
		c.fijar(Estado{Estado: "INSTALANDO"})
		if err := c.ops.Instalar(); err != nil {
			falla("no se pudo instalar RustDesk", err)
			return
		}
		c.mu.Lock()
		c.propio = true
		c.mu.Unlock()
		c.alInstalar()
		encendido = false
	}

	c.mu.Lock()
	propio := c.propio
	c.mu.Unlock()
	if propio {
		// Se configura con el servicio apagado (ver Configurar).
		if encendido {
			_ = c.ops.Apagar()
		}
		if err := c.ops.Configurar(serv); err != nil {
			falla("no se pudo configurar RustDesk", err)
			return
		}
		c.mu.Lock()
		c.servidor = &serv
		c.mu.Unlock()
	} else if !encendido {
		c.mu.Lock()
		c.arrancamos = true
		c.mu.Unlock()
	}
	id, err := c.ops.Encender()
	if err != nil {
		falla("no se pudo encender RustDesk", err)
		return
	}
	c.log.Printf("soporte remoto: encendido (ID %s)", id)
	c.fijar(Estado{Estado: "ACTIVO", ID: id, Version: Version})
}

func (c *Controlador) version() string {
	if c.ops.Instalado() {
		return Version
	}
	return ""
}

// sistema son las operaciones reales.
type sistema struct{}

func (sistema) Instalado() bool              { return Instalado() }
func (sistema) Instalar() error              { return Instalar(DirDescargas, nil) }
func (sistema) Configurar(sv Servidor) error { return Configurar(sv) }
func (sistema) Encender() (string, error)    { return Encender() }
func (sistema) LeerID() (string, error)      { return LeerID() }
func (sistema) Apagar() error                { return Apagar() }
func (sistema) Encendido() bool              { return Encendido() }
func (sistema) SesionActiva() bool           { return SesionActiva() }
func (sistema) AsegurarInicioManual() error  { return AsegurarInicioManual() }

// DirDescargas es donde el servicio baja el MSI (lo fija el main: la carpeta
// de datos del agente, que solo pueden tocar SYSTEM y Administradores).
var DirDescargas = ""
