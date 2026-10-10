// Package agent es el ciclo del servicio. Un equipo puede estar vinculado con
// VARIOS servicios técnicos: CorrerTodos levanta un Agente por vínculo, y cada
// uno corre dos cosas a la vez:
//
//   - escucharPedidos: pregunta al backend de SU empresa "¿hay pedido?"
//     (long-poll de ~25 s). Cuando alguien pide los datos desde el panel, toma
//     un reporte y se lo envía SOLO a esa empresa. La respuesta también trae el
//     modo (a pedido o continuo).
//   - reportarPeriodico: SOLO en modo continuo, toma un reporte cada intervalo.
//   - informarSoporte: si el cliente autorizó el soporte remoto a esa empresa,
//     le avisa cómo está RustDesk (apagado, encendido con su ID...). El pedido
//     de encenderlo llega en la misma respuesta de "¿hay pedido?".
//
// A pedido (el modo por defecto) el equipo no manda reportes que nadie pidió.
// Cada reporte se encola en disco (una cola por vínculo) y la cola se vacía
// contra el backend, del más viejo al más nuevo. Sin internet, esperan en la
// cola (máximo maxCola). Si una empresa desvincula el equipo, se quita solo
// ese vínculo: los demás siguen.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hellares/syncronize-agent/internal/client"
	"github.com/Hellares/syncronize-agent/internal/collector"
	"github.com/Hellares/syncronize-agent/internal/config"
	"github.com/Hellares/syncronize-agent/internal/remoto"
)

// 50 reportes = ~12 h sin conexión a 15 min. Más viejo que eso no aporta.
const maxCola = 50

const (
	// Entre dos reportes a pedido: si el backend insiste con el mismo pedido
	// (p. ej. rechazó el reporte), el agente no entra en un bucle.
	separacionPedidos = 30 * time.Second
	// Servidor sin /agentes/pedido: se vuelve a probar cada tanto.
	reintentoSinPedidos = 10 * time.Minute
	esperaMaximaError   = 5 * time.Minute
	// Sin red para avisar el estado del soporte remoto: se reintenta.
	reintentoSoporte = time.Minute
)

// Leer el equipo (WMI, impresoras) de a uno: dos empresas pidiendo a la vez
// no duplican la carga.
var recolectando sync.Mutex

// pedidos es lo que el agente usa del cliente para escuchar (en los tests, un falso).
type pedidos interface {
	EsperarPedido(ctx context.Context, token string) (*client.RespuestaPedido, error)
}

// Agente atiende UN vínculo (una empresa).
type Agente struct {
	v   *config.Vinculo
	cli *client.Client
	ped pedidos
	// Soporte remoto (uno por equipo, compartido); nil en `send`.
	ctrl    *remoto.Controlador
	version string
	log     *log.Logger

	mu sync.Mutex // un reporte a la vez por vínculo
	// Compartido entre los vínculos: los campos de cada uno y guardar la config.
	cfgMu *sync.Mutex
	// El servidor no atiende pedidos (versión vieja): se reporta cada intervalo.
	sinPedidos bool
	// Para reportar es el ciclo y para guardar, la config entera; en los tests, falsos.
	reportar func() error
	guardar  func() error
}

func nuevo(v *config.Vinculo, cfgMu *sync.Mutex, guardar func() error, ctrl *remoto.Controlador, version string, l *log.Logger) *Agente {
	cli := client.New(v.Servidor, version)
	pref := log.New(l.Writer(), l.Prefix()+"["+v.Nombre()+"] ", l.Flags())
	a := &Agente{v: v, cli: cli, ped: cli, ctrl: ctrl, version: version, log: pref, cfgMu: cfgMu, guardar: guardar}
	a.reportar = a.Ciclo
	return a
}

// CorrerTodos atiende todos los vínculos de cfg hasta que ctx se cancele o no
// quede ninguno. Un vínculo revocado se quita de la config; los demás siguen.
func CorrerTodos(ctx context.Context, cfg *config.Config, version string, l *log.Logger) error {
	cfgMu := &sync.Mutex{}
	guardar := func() error { return config.Guardar(cfg) } // se llama con cfgMu tomado
	migrarColaVieja(cfg)

	// Un solo RustDesk por equipo, para todos los servicios técnicos autorizados.
	remoto.DirDescargas = config.Dir()
	if !cfg.RustDeskPropio && remoto.InstaladoPorAgente(config.Dir()) {
		cfgMu.Lock()
		cfg.RustDeskPropio = true
		_ = guardar()
		cfgMu.Unlock()
	}
	ctrl := remoto.Nuevo(cfg.RustDeskPropio, cfg.ConSoporte(), func() {
		cfgMu.Lock()
		cfg.RustDeskPropio = true
		_ = guardar()
		cfgMu.Unlock()
	}, l)
	go ctrl.Correr(ctx)
	go quitarSoporteSiNadieLoAutoriza(cfg, cfgMu, guardar, l)

	fin := make(chan *config.Vinculo, len(cfg.Vinculos))
	cfgMu.Lock()
	vinculos := append([]*config.Vinculo(nil), cfg.Vinculos...)
	cfgMu.Unlock()
	for _, v := range vinculos {
		a := nuevo(v, cfgMu, guardar, ctrl, version, l)
		go func() {
			if errors.Is(a.Correr(ctx), client.ErrRevocado) {
				a.log.Printf("desvinculado desde Syncronize: este servicio técnico deja de recibir datos")
				fin <- v
				return
			}
			fin <- nil
		}()
	}
	for quedan := len(vinculos); quedan > 0; quedan-- {
		if v := <-fin; v != nil {
			cfgMu.Lock()
			cfg.Quitar(v)
			_ = guardar()
			cfgMu.Unlock()
			_ = os.RemoveAll(config.DirCola(v.AgenteID))
			if v.SoporteAutorizado() {
				ctrl.Olvidar(v.AgenteID)
				quitarSoporteSiNadieLoAutoriza(cfg, cfgMu, guardar, l)
			}
		}
	}
	return nil
}

// quitarSoporteSiNadieLoAutoriza desinstala RustDesk si lo puso el agente y ya
// ningún servicio técnico tiene autorización (el cliente la retiró, o lo
// desvincularon).
func quitarSoporteSiNadieLoAutoriza(cfg *config.Config, cfgMu *sync.Mutex, guardar func() error, l *log.Logger) {
	cfgMu.Lock()
	quitar := cfg.RustDeskPropio && cfg.ConSoporte() == 0
	cfgMu.Unlock()
	if !quitar {
		return
	}
	if remoto.Instalado() {
		l.Printf("soporte remoto: nadie lo tiene autorizado, se desinstala RustDesk")
		if err := remoto.Desinstalar(); err != nil {
			l.Printf("soporte remoto: no se pudo desinstalar: %v", err)
			return
		}
	}
	cfgMu.Lock()
	cfg.RustDeskPropio = false
	_ = guardar()
	cfgMu.Unlock()
}

// EnviarATodos toma un reporte por vínculo y lo envía (comando `send`).
func EnviarATodos(cfg *config.Config, version string, l *log.Logger) error {
	cfgMu := &sync.Mutex{}
	guardar := func() error { return config.Guardar(cfg) }
	var errs []error
	for _, v := range cfg.Vinculos {
		if err := nuevo(v, cfgMu, guardar, nil, version, l).Ciclo(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", v.Nombre(), err))
		}
	}
	return errors.Join(errs...)
}

// Correr bloquea hasta que ctx se cancele o el vínculo sea revocado (ErrRevocado).
func (a *Agente) Correr(ctx context.Context) error {
	ctx, cancelar := context.WithCancel(ctx)
	defer cancelar()
	errc := make(chan error, 3)
	go func() { errc <- a.escucharPedidos(ctx) }()
	go func() { errc <- a.reportarPeriodico(ctx) }()
	go func() { errc <- a.informarSoporte(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		if errors.Is(err, client.ErrRevocado) {
			return err
		}
		return nil
	}
}

// continuo dice si hay que reportar cada intervalo.
func (a *Agente) continuo() bool {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.v.Continuo || a.sinPedidos
}

func (a *Agente) intervalo() time.Duration {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return time.Duration(a.v.Intervalo()) * time.Second
}

// aplicarModo guarda lo que manda el backend. Devuelve true si el vínculo
// ACABA de pasar a continuo (se reporta en el acto, sin esperar el intervalo).
func (a *Agente) aplicarModo(continuo bool, intervaloSegundos int) bool {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	antes := a.v.Continuo || a.sinPedidos
	a.sinPedidos = false
	cambio := a.v.Continuo != continuo
	if intervaloSegundos > 0 && intervaloSegundos != a.v.IntervaloSegundos {
		a.v.IntervaloSegundos = intervaloSegundos
		cambio = true
	}
	if cambio {
		a.v.Continuo = continuo
		_ = a.guardar()
		a.log.Printf("modo: %s", map[bool]string{true: "continuo", false: "a pedido"}[continuo])
	}
	return continuo && !antes
}

func (a *Agente) escucharPedidos(ctx context.Context) error {
	var espera time.Duration
	var ultimoPedido time.Time
	for {
		if !dormir(ctx, espera) {
			return nil
		}
		espera = 0
		res, err := a.ped.EsperarPedido(ctx, a.v.Token)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, client.ErrRevocado):
			return err
		case errors.Is(err, client.ErrSinPedidos):
			a.cfgMu.Lock()
			if !a.sinPedidos {
				a.log.Printf("el servidor no atiende pedidos: reporta cada intervalo")
			}
			a.sinPedidos = true
			a.cfgMu.Unlock()
			espera = reintentoSinPedidos
			continue
		case err != nil:
			// Sin red o servidor caído: reintenta cada vez más espaciado.
			a.log.Printf("pedidos: %v", err)
			espera = min(max(2*espera, 15*time.Second), esperaMaximaError)
			continue
		}
		a.pedirSoporte(res.SoporteRemoto)
		pasoAContinuo := a.aplicarModo(res.Continuo, res.IntervaloSegundos)
		if !res.Reportar && !pasoAContinuo {
			continue
		}
		if d := separacionPedidos - time.Since(ultimoPedido); d > 0 && !dormir(ctx, d) {
			return nil
		}
		ultimoPedido = time.Now()
		if res.Reportar {
			a.log.Printf("pedido de datos desde el panel")
		}
		if err := a.reportar(); err != nil {
			if errors.Is(err, client.ErrRevocado) {
				return err
			}
			a.log.Printf("reporte a pedido: %v", err)
		}
	}
}

// pedirSoporte le pasa al controlador lo que el panel quiere del soporte
// remoto. Solo cuenta si el cliente autorizó a ESTE servicio técnico.
func (a *Agente) pedirSoporte(p *client.PedidoSoporte) {
	if a.ctrl == nil || !a.v.SoporteAutorizado() || p == nil {
		return
	}
	pedido := remoto.Pedido{Servidor: remoto.Servidor{Host: p.Host, Key: p.Key}}
	if p.RestanteSegundos != nil {
		hasta := time.Now().Add(time.Duration(*p.RestanteSegundos) * time.Second)
		pedido.Hasta = &hasta
	}
	a.ctrl.Pedir(a.v.AgenteID, pedido)
}

// informarSoporte le cuenta al backend de esta empresa si el cliente autorizó
// el soporte remoto y, si sí, cómo está RustDesk cada vez que cambia. Solo
// vuelve con ctx cancelado o el vínculo revocado.
func (a *Agente) informarSoporte(ctx context.Context) error {
	autorizado := a.ctrl != nil && a.v.SoporteAutorizado()
	var enviado *remoto.Estado
	for {
		e, cambio := remoto.Estado{}, (<-chan struct{})(nil)
		if autorizado {
			e, cambio = a.ctrl.Estado()
		}
		if enviado == nil || *enviado != e {
			err := a.cli.InformarSoporte(a.v.Token, client.EstadoSoporte{
				Permitido: autorizado, Estado: e.Estado, ID: e.ID, Detalle: e.Detalle, Version: e.Version,
			})
			switch {
			case errors.Is(err, client.ErrRevocado):
				return err
			case errors.Is(err, client.ErrSinPedidos):
				// Servidor sin soporte remoto: nada que avisar.
				<-ctx.Done()
				return nil
			case err != nil:
				a.log.Printf("soporte remoto: %v", err)
				if !dormir(ctx, reintentoSoporte) {
					return nil
				}
				continue
			}
			enviado = &e
		}
		// Sin autorización, el aviso es uno solo; cambio == nil bloquea.
		select {
		case <-ctx.Done():
			return nil
		case <-cambio:
		}
	}
}

func (a *Agente) reportarPeriodico(ctx context.Context) error {
	// Arranque escalonado: si se reinician 50 PCs a la vez no golpean juntas.
	espera := time.Duration(5+rand.Intn(25)) * time.Second
	for {
		if !dormir(ctx, espera) {
			return nil
		}
		espera = a.intervalo()
		if !a.continuo() {
			continue
		}
		if err := a.reportar(); err != nil {
			if errors.Is(err, client.ErrRevocado) {
				return err
			}
			a.log.Printf("ciclo: %v", err)
		}
	}
}

// dormir espera d (0 = nada). Devuelve false si ctx se canceló.
func dormir(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Ciclo toma un reporte, lo encola y vacía la cola de este vínculo.
func (a *Agente) Ciclo() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	recolectando.Lock()
	r := collector.Recolectar(a.version)
	recolectando.Unlock()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := encolar(a.v.AgenteID, b, r.TomadoEn); err != nil {
		return fmt.Errorf("encolar: %w", err)
	}
	return a.vaciarCola()
}

// vaciarCola se llama con a.mu tomado.
func (a *Agente) vaciarCola() error {
	pendientes, err := listarCola(a.v.AgenteID)
	if err != nil {
		return err
	}
	for _, f := range pendientes {
		b, err := os.ReadFile(f)
		if err != nil {
			_ = os.Remove(f)
			continue
		}
		res, err := a.cli.EnviarReporte(a.v.Token, b)
		if errors.Is(err, client.ErrRechazado) {
			a.log.Printf("descartado %s: %v", filepath.Base(f), err)
			_ = os.Remove(f)
			continue
		}
		if err != nil {
			// Sin red o servidor caído: queda en cola para el próximo reporte.
			return fmt.Errorf("enviar (%d en cola): %w", len(pendientes), err)
		}
		_ = os.Remove(f)
		a.cfgMu.Lock()
		cambio := false
		// El backend puede cambiar el intervalo (p. ej. plan con más frecuencia).
		if res.IntervaloSegundos > 0 && res.IntervaloSegundos != a.v.IntervaloSegundos {
			a.v.IntervaloSegundos = res.IntervaloSegundos
			cambio = true
		}
		if res.Continuo != nil && *res.Continuo != a.v.Continuo {
			a.v.Continuo = *res.Continuo
			cambio = true
		}
		if cambio {
			_ = a.guardar()
		}
		a.cfgMu.Unlock()
	}
	return nil
}

// migrarColaVieja: v0.2-v0.4 dejaban la cola en cola\*.json (un solo
// vínculo). Esos reportes pasan a la cola del primer vínculo.
func migrarColaVieja(cfg *config.Config) {
	if len(cfg.Vinculos) == 0 {
		return
	}
	entradas, err := os.ReadDir(config.DirColaRaiz())
	if err != nil {
		return
	}
	destino := config.DirCola(cfg.Vinculos[0].AgenteID)
	for _, e := range entradas {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		_ = os.MkdirAll(destino, 0o700)
		_ = os.Rename(filepath.Join(config.DirColaRaiz(), e.Name()), filepath.Join(destino, e.Name()))
	}
}

func encolar(agenteID string, b []byte, t time.Time) error {
	dir := config.DirCola(agenteID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	nombre := filepath.Join(dir, t.UTC().Format("20060102T150405.000000000")+".json")
	if err := os.WriteFile(nombre, b, 0o600); err != nil {
		return err
	}
	// Recorta: se van los más viejos.
	pendientes, err := listarCola(agenteID)
	if err != nil {
		return err
	}
	for len(pendientes) > maxCola {
		_ = os.Remove(pendientes[0])
		pendientes = pendientes[1:]
	}
	return nil
}

// listarCola devuelve los archivos de la cola de un vínculo, del más viejo al
// más nuevo (el nombre es la fecha: el orden alfabético es el cronológico).
func listarCola(agenteID string) ([]string, error) {
	dir := config.DirCola(agenteID)
	entradas, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entradas {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// EnCola cuenta los reportes pendientes de envío de un vínculo (para `status`).
func EnCola(agenteID string) int {
	p, _ := listarCola(agenteID)
	return len(p)
}
