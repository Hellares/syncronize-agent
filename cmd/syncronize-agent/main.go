// syncronize-agent: agente de monitoreo para Windows (solo lectura).
//
// Lo instala el técnico con el consentimiento del cliente:
//
//	syncronize-agent enroll --codigo ABC123     vincula, instala el servicio y lo arranca
//
// Ver `syncronize-agent help` para el resto de comandos.
package main

import (
	"bufio"
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
	"github.com/Hellares/syncronize-agent/internal/remoto"
)

// Version la pisa el build: -ldflags "-X main.Version=x.y.z".
var Version = "0.1.0-dev"

const ayuda = `Uso: syncronize-agent <comando>

  (doble clic)  pide permisos de administrador y el código, y vincula
  enroll --codigo XXXX [--beta | --servidor URL] [--soporte-remoto si|no] [--resultado ARCHIVO]
            vincula el equipo, se instala en Program Files y arranca el servicio
            (--soporte-remoto: lo que decidió el cliente; sin él, queda sin preguntar)
  actualizar [--resultado ARCHIVO]
            instala o actualiza el servicio desde Program Files y lo arranca
  vinculos --resultado ARCHIVO
            lista los servicios técnicos vinculados (lo usa el instalador)
  status    muestra la vinculación, el servicio y los reportes en cola
  soporte [permitir | quitar]
            estado del soporte remoto (RustDesk); permitir/quitar cambia la
            autorización de TODOS los servicios técnicos
  collect   imprime el reporte del equipo en JSON (no envía nada)
  send      toma un reporte y lo envía ahora (prueba de conexión)
  start | stop | restart
  uninstall [--conservar-archivos]
            detiene el servicio, lo quita y BORRA la vinculación (y RustDesk si
            lo instaló el agente); --conservar-archivos deja Program Files al
            desinstalador de Windows
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
	case "actualizar":
		return actualizar(args)
	case "vinculos":
		return listarVinculos(args)
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
		return desinstalar(args)
	case "soporte":
		return soporte(args)
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
	conSoporte := fs.String("soporte-remoto", "", "si | no: lo que decidió el cliente")
	resultado := fs.String("resultado", "", "archivo donde dejar el resultado (para el instalador)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *codigo == "" {
		return escribirResultado(*resultado, "", errors.New("falta --codigo (se genera en Syncronize, ficha del cliente → Monitoreo)"))
	}
	if err := exigirAdmin(); err != nil {
		return escribirResultado(*resultado, "", err)
	}
	url := servidorPorDefecto()
	if *beta {
		url = config.ServidorBeta
	}
	if *servidor != "" {
		url = *servidor
	}
	// Sin --soporte-remoto queda sin preguntar: el asistente lo pregunta después.
	var soporte *bool
	switch strings.ToLower(*conSoporte) {
	case "si", "sí", "true", "s":
		si := true
		soporte = &si
	case "no", "false", "n":
		no := false
		soporte = &no
	}
	resumen, err := vincularConCodigo(*codigo, url, nil, soporte)
	return escribirResultado(*resultado, resumen, err)
}

// actualizar instala o actualiza el servicio (el instalador de Windows ya
// dejó el .exe nuevo en Program Files) y lo arranca.
func actualizar(args []string) error {
	fs := flag.NewFlagSet("actualizar", flag.ContinueOnError)
	resultado := fs.String("resultado", "", "archivo donde dejar el resultado")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := exigirAdmin(); err != nil {
		return escribirResultado(*resultado, "", err)
	}
	if err := instalarYArrancar(); err != nil {
		return escribirResultado(*resultado, "", err)
	}
	return escribirResultado(*resultado, "Agente actualizado a la versión "+Version+" y corriendo.", nil)
}

// listarVinculos deja una línea por vínculo: "empresa<TAB>soporte", con
// soporte = si | no | ? (sin preguntar). Sin vínculos, el archivo queda vacío.
func listarVinculos(args []string) error {
	fs := flag.NewFlagSet("vinculos", flag.ContinueOnError)
	resultado := fs.String("resultado", "", "archivo donde dejar la lista")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var b strings.Builder
	if cfg, err := config.Cargar(); err == nil {
		for _, v := range cfg.Vinculos {
			sop := "?"
			if v.SoporteRemoto != nil {
				sop = map[bool]string{true: "si", false: "no"}[*v.SoporteRemoto]
			}
			fmt.Fprintf(&b, "%s\t%s\r\n", v.Nombre(), sop)
		}
	}
	if *resultado == "" {
		fmt.Print(b.String())
		return nil
	}
	return escribirArchivoUTF8(*resultado, b.String())
}

// escribirResultado deja para el instalador "OK" o "ERROR" en la 1.ª línea y
// el mensaje para mostrar al cliente en las siguientes. Devuelve err tal cual.
func escribirResultado(ruta, resumen string, err error) error {
	if ruta == "" {
		return err
	}
	texto := "OK\r\n" + resumen
	if err != nil {
		texto = "ERROR\r\n" + mensajeParaCliente(err)
	}
	if e := escribirArchivoUTF8(ruta, texto); e != nil && err == nil {
		return e
	}
	return err
}

// mensajeParaCliente: primera letra en mayúscula y sin saltos de línea.
func mensajeParaCliente(err error) string {
	m := strings.Join(strings.Fields(err.Error()), " ")
	if m == "" {
		return "Error desconocido"
	}
	r := []rune(m)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// escribirArchivoUTF8 con BOM: así el instalador (Inno Setup) lee bien las tildes.
func escribirArchivoUTF8(ruta, texto string) error {
	return os.WriteFile(ruta, append([]byte{0xEF, 0xBB, 0xBF}, texto...), 0o600)
}

// vincularConCodigo canjea el código y AGREGA el vínculo (los que ya había,
// con otros servicios técnicos, siguen). Pregunta por el soporte remoto (con
// lector, en el asistente) o usa lo que diga soporte (en `enroll`). Instala
// el agente en Program Files y (re)arranca el servicio para que tome el
// vínculo nuevo. Pide admin.
func vincularConCodigo(codigo, url string, lector *bufio.Reader, soporte *bool) (string, error) {
	fmt.Println("Leyendo el equipo...")
	r := collector.Recolectar(Version)
	// Volver a vincular con la misma empresa le cambia el token: con el
	// servicio corriendo, el token viejo recibiría 401 y el servicio se
	// desvincularía solo. Se para antes y se vuelve a arrancar al final.
	detenido := detenerSiCorre()
	fmt.Printf("Vinculando %s (%s %s)...\n", r.Equipo.Hostname, r.Equipo.Fabricante, r.Equipo.Modelo)
	res, err := client.New(url, Version).Vincular(strings.ToUpper(strings.TrimSpace(codigo)), r)
	if err != nil {
		if detenido {
			_ = nuevoServicio().Start()
		}
		return "", err
	}
	cfg, err := config.Cargar()
	if err != nil && !errors.Is(err, config.ErrSinVincular) {
		return "", err
	}
	v := &config.Vinculo{
		Servidor: url, AgenteID: res.AgenteID, Token: res.Token, Empresa: res.Empresa,
		VinculadoEn: time.Now().UTC(), IntervaloSegundos: res.IntervaloSegundos,
		SoporteRemoto: soporte,
	}
	cfg.Agregar(v)
	if lector != nil {
		preguntarSoporte(lector, v)
	}
	notaSoporte := prepararSoporte(cfg)
	if err := config.Guardar(cfg); err != nil {
		return "", err
	}
	fmt.Println("Instalando el servicio...")
	if err := instalarYArrancar(); err != nil {
		return "", fmt.Errorf("vinculado, pero %w", err)
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
	resumen := "El equipo quedó vinculado con " + nombre + "."
	if notaSoporte != "" {
		resumen += "\r\n" + notaSoporte
	}
	return resumen, nil
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

func desinstalar(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	conservar := fs.Bool("conservar-archivos", false, "no borrar Program Files (lo hace el desinstalador de Windows)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := exigirAdmin(); err != nil {
		return err
	}
	s := nuevoServicio()
	_ = s.Stop()
	if err := s.Uninstall(); err != nil && !strings.Contains(err.Error(), "not installed") && !errors.Is(err, service.ErrNotInstalled) {
		return err
	}
	esperarDetenido(s)
	if cfg, err := config.Cargar(); err == nil && cfg.RustDeskPropio && remoto.Instalado() {
		fmt.Println("Quitando el soporte remoto (RustDesk)...")
		if err := remoto.Desinstalar(); err != nil {
			fmt.Println("No se pudo quitar RustDesk:", err)
		}
	}
	if err := config.Borrar(); err != nil {
		return err
	}
	if !*conservar {
		borrarInstalacion()
	}
	fmt.Println("Servicio quitado, vínculos borrados y agente desinstalado.")
	return nil
}

// soporte muestra el estado del soporte remoto o cambia la autorización.
func soporte(args []string) error {
	cfg, err := config.Cargar()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		if args[0] != "permitir" && args[0] != "quitar" {
			return fmt.Errorf("uso: soporte [permitir | quitar]")
		}
		if err := exigirAdmin(); err != nil {
			return err
		}
		si := args[0] == "permitir"
		for _, v := range cfg.Vinculos {
			v.SoporteRemoto = &si
		}
		prepararSoporte(cfg)
		if err := config.Guardar(cfg); err != nil {
			return err
		}
		// El servicio toma la autorización nueva (y desinstala RustDesk si ya
		// nadie lo tiene autorizado).
		// Restart falla si el servicio estaba detenido: se para (si corre) y se arranca.
		detenerSiCorre()
		if err := nuevoServicio().Start(); err != nil {
			return fmt.Errorf("autorización guardada, pero el servicio no arrancó: %w", err)
		}
	}
	fmt.Println("Soporte remoto:")
	for _, v := range cfg.Vinculos {
		autorizado := map[bool]string{true: "autorizado", false: "no autorizado"}[v.SoporteAutorizado()]
		if v.SoporteRemoto == nil {
			autorizado = "sin preguntar"
		}
		fmt.Printf("  - %s: %s\n", v.Nombre(), autorizado)
	}
	if !remoto.Instalado() {
		fmt.Println("RustDesk:   no instalado")
		return nil
	}
	de := "lo instaló el agente"
	if !cfg.RustDeskPropio {
		de = "ya estaba en el equipo (no se toca)"
	}
	fmt.Printf("RustDesk:   instalado (%s)\n", de)
	if !remoto.Encendido() {
		fmt.Println("Estado:     apagado (nadie se puede conectar)")
		return nil
	}
	id, err := remoto.LeerID()
	if err != nil {
		id = err.Error()
	}
	fmt.Println("Estado:     ENCENDIDO, ID", id)
	if remoto.SesionActiva() {
		fmt.Println("            hay un técnico conectado")
	}
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
		if ctx.Err() != nil {
			l.Printf("agente detenido")
		} else {
			l.Printf("sin vínculos activos: el agente queda en espera")
		}
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
