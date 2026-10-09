// syncronize-agent: agente de monitoreo para Windows (solo lectura).
//
// Lo instala el técnico con el consentimiento del cliente:
//
//	syncronize-agent enroll --codigo ABC123     vincula, instala el servicio y lo arranca
//
// Ver `syncronize-agent help` para el resto de comandos.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"

	"github.com/Hellares/syncronize-agent/internal/agent"
	"github.com/Hellares/syncronize-agent/internal/client"
	"github.com/Hellares/syncronize-agent/internal/collector"
	"github.com/Hellares/syncronize-agent/internal/config"
)

// Version la pisa el build: -ldflags "-X main.Version=x.y.z".
var Version = "0.1.0-dev"

const ayuda = `Uso: syncronize-agent <comando>

  (doble clic)  pide permisos de administrador y el código, y vincula
  enroll --codigo XXXX [--beta | --servidor URL]
            vincula el equipo, se instala en Program Files y arranca el servicio
  status    muestra la vinculación, el servicio y los reportes en cola
  collect   imprime el reporte del equipo en JSON (no envía nada)
  send      toma un reporte y lo envía ahora (prueba de conexión)
  start | stop | restart
  uninstall detiene el servicio, lo quita y BORRA la vinculación
  run       corre en primer plano (diagnóstico)
  version

enroll, send, start, stop, restart y uninstall piden una consola de administrador.`

func main() {
	if len(os.Args) < 2 {
		asistente()
		return
	}
	// Lanzado por el Administrador de servicios de Windows.
	if os.Args[1] == "service" {
		if err := nuevoServicio().Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := ejecutar(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func ejecutar(cmd string, args []string) error {
	switch cmd {
	case "collect":
		return imprimirJSON(collector.Recolectar(Version))
	case "version":
		fmt.Printf("%s (servidor por defecto: %s)\n", Version, servidorPorDefecto())
		return nil
	case "status":
		return estado()
	case "enroll":
		return vincular(args)
	case "send":
		if err := exigirAdmin(); err != nil {
			return err
		}
		cfg, err := config.Cargar()
		if err != nil {
			return err
		}
		if err := agent.EnviarATodos(cfg, Version, log.New(os.Stdout, "", log.LstdFlags)); err != nil {
			return err
		}
		fmt.Printf("Reporte enviado a %d servicio(s) técnico(s).\n", len(cfg.Vinculos))
		return nil
	case "start", "stop", "restart":
		if err := exigirAdmin(); err != nil {
			return err
		}
		return service.Control(nuevoServicio(), cmd)
	case "uninstall":
		return desinstalar()
	case "run":
		return correrEnPrimerPlano()
	case "help", "-h", "--help":
		fmt.Println(ayuda)
		return nil
	}
	return fmt.Errorf("comando desconocido %q\n\n%s", cmd, ayuda)
}

// ── enroll ──────────────────────────────────────────────────────────────

func vincular(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	codigo := fs.String("codigo", "", "código de vinculación generado en Syncronize")
	beta := fs.Bool("beta", false, "usar el servidor de pruebas")
	servidor := fs.String("servidor", "", "URL del API (por defecto, producción)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *codigo == "" {
		return errors.New("falta --codigo (se genera en Syncronize, ficha del cliente → Monitoreo)")
	}
	if err := exigirAdmin(); err != nil {
		return err
	}
	url := servidorPorDefecto()
	if *beta {
		url = config.ServidorBeta
	}
	if *servidor != "" {
		url = *servidor
	}
	return vincularConCodigo(*codigo, url)
}

// vincularConCodigo canjea el código y AGREGA el vínculo (los que ya había,
// con otros servicios técnicos, siguen). Instala el agente en Program Files y
// (re)arranca el servicio para que tome el vínculo nuevo. Pide admin.
func vincularConCodigo(codigo, url string) error {
	fmt.Println("Leyendo el equipo...")
	r := collector.Recolectar(Version)
	fmt.Printf("Vinculando %s (%s %s)...\n", r.Equipo.Hostname, r.Equipo.Fabricante, r.Equipo.Modelo)
	res, err := client.New(url, Version).Vincular(strings.ToUpper(strings.TrimSpace(codigo)), r)
	if err != nil {
		return err
	}
	cfg, err := config.Cargar()
	if err != nil && !errors.Is(err, config.ErrSinVincular) {
		return err
	}
	cfg.Agregar(&config.Vinculo{
		Servidor: url, AgenteID: res.AgenteID, Token: res.Token, Empresa: res.Empresa,
		VinculadoEn: time.Now().UTC(), IntervaloSegundos: res.IntervaloSegundos,
	})
	if err := config.Guardar(cfg); err != nil {
		return err
	}
	fmt.Println("Instalando el servicio...")
	if err := instalarYArrancar(); err != nil {
		return fmt.Errorf("vinculado, pero %w", err)
	}
	nombre := res.Empresa
	if nombre == "" {
		nombre = "el servicio técnico"
	}
	fmt.Printf("Listo: el equipo quedó vinculado con %s y el servicio está corriendo.\n", nombre)
	if len(cfg.Vinculos) > 1 {
		fmt.Printf("Está vinculado con %d servicios técnicos; cada uno ve solo lo suyo.\n", len(cfg.Vinculos))
	}
	fmt.Println("Instalado en", dirInstalacion())
	return nil
}

// ── status / uninstall ──────────────────────────────────────────────────

func estado() error {
	fmt.Println("Versión:   ", Version)
	cfg, err := config.Cargar()
	switch {
	case errors.Is(err, config.ErrSinVincular):
		fmt.Println("Vinculado:  no")
	case err != nil:
		fmt.Println("Vinculado: ", err)
	default:
		fmt.Printf("Vinculado con %d servicio(s) técnico(s):\n", len(cfg.Vinculos))
		for _, v := range cfg.Vinculos {
			desde := ""
			if !v.VinculadoEn.IsZero() {
				desde = " desde " + v.VinculadoEn.Local().Format("02/01/2006")
			}
			fmt.Printf("  - %s%s · %s · %s · en cola: %d\n", v.Nombre(), desde, modo(v), nombreServidor(v.Servidor), agent.EnCola(v.AgenteID))
		}
	}
	st, err := nuevoServicio().Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		fmt.Println("Servicio:   no instalado")
	case err != nil:
		fmt.Println("Servicio:  ", err)
	default:
		fmt.Println("Servicio:  ", map[service.Status]string{service.StatusRunning: "corriendo", service.StatusStopped: "detenido"}[st])
	}
	return nil
}

func desinstalar() error {
	if err := exigirAdmin(); err != nil {
		return err
	}
	s := nuevoServicio()
	_ = s.Stop()
	if err := s.Uninstall(); err != nil && !strings.Contains(err.Error(), "not installed") && !errors.Is(err, service.ErrNotInstalled) {
		return err
	}
	esperarDetenido(s)
	if err := config.Borrar(); err != nil {
		return err
	}
	borrarInstalacion()
	fmt.Println("Servicio quitado, vínculos borrados y agente desinstalado.")
	return nil
}

// ── servicio ────────────────────────────────────────────────────────────

type programa struct {
	cancelar context.CancelFunc
	fin      chan struct{}
}

func (p *programa) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelar = cancel
	p.fin = make(chan struct{})
	go func() {
		defer close(p.fin)
		l := abrirLog()
		cfg, err := config.Cargar()
		if err != nil {
			l.Printf("no arranca: %v", err)
			return
		}
		l.Printf("agente %s iniciado: %d vínculo(s)", Version, len(cfg.Vinculos))
		for _, v := range cfg.Vinculos {
			l.Printf("  %s (%s)", v.Nombre(), modo(v))
		}
		_ = agent.CorrerTodos(ctx, cfg, Version, l)
		l.Printf("sin vínculos activos: el agente queda en espera")
	}()
	return nil
}

func (p *programa) Stop(s service.Service) error {
	p.cancelar()
	<-p.fin
	return nil
}

func nuevoServicio() service.Service {
	s, err := service.New(&programa{}, &service.Config{
		Name:        "SyncronizeAgent",
		DisplayName: "Syncronize - Agente de monitoreo",
		Description: "Reporta el estado técnico del equipo (discos, batería, memoria, antivirus) a su servicio técnico. No lee archivos personales.",
		Executable:  rutaInstalada(),
		Arguments:   []string{"service"},
		Option: service.KeyValue{
			"StartType":              "automatic",
			"DelayedAutoStart":       true,
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "1m",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return s
}

func correrEnPrimerPlano() error {
	cfg, err := config.Cargar()
	if err != nil {
		return err
	}
	l := log.New(os.Stdout, "", log.LstdFlags)
	l.Printf("corriendo en primer plano con %d vínculo(s) (Ctrl+C para salir)", len(cfg.Vinculos))
	return agent.CorrerTodos(context.Background(), cfg, Version, l)
}

func modo(v *config.Vinculo) string {
	if v.Continuo {
		return fmt.Sprintf("continuo, cada %d min", v.Intervalo()/60)
	}
	return "a pedido"
}

// abrirLog escribe en %ProgramData%\Syncronize\Agent\agent.log; pasando 1 MB
// lo rota a agent.log.1 (queda como mucho ~2 MB).
func abrirLog() *log.Logger {
	ruta := filepath.Join(config.Dir(), "agent.log")
	if fi, err := os.Stat(ruta); err == nil && fi.Size() > 1<<20 {
		_ = os.Rename(ruta, ruta+".1")
	}
	var w io.Writer = io.Discard
	if f, err := os.OpenFile(ruta, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		w = f
	}
	return log.New(w, "", log.LstdFlags)
}

// ── utilidades ──────────────────────────────────────────────────────────

func exigirAdmin() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return errors.New("este comando necesita una consola de administrador (clic derecho → Ejecutar como administrador)")
}

func imprimirJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
