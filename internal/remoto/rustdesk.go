// Package remoto maneja RustDesk, el programa de soporte remoto (código
// abierto, AGPL) que el agente instala SOLO si el cliente lo autoriza.
//
// Reglas:
//   - Se instala el MSI OFICIAL de RustDesk (versión fija, se verifica su
//     SHA-256): el agente no lo modifica ni lo reempaqueta.
//   - Queda APAGADO (servicio en inicio manual). Se enciende solo cuando un
//     servicio técnico autorizado lo pide desde el panel, y se apaga al
//     terminar o al vencer el plazo.
//   - El cliente ACEPTA cada conexión con un clic (approve-mode = click): no
//     hay contraseña que permita entrar sin que él lo vea.
//   - Si el equipo ya tenía su propio RustDesk, no se toca su configuración ni
//     se desinstala.
package remoto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/yusufpapurcu/wmi"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Versión fija de RustDesk. Para subirla: cambiar las tres constantes con los
// datos del release (https://github.com/rustdesk/rustdesk/releases, campo
// "digest" del .msi x86_64).
const (
	Version   = "1.4.9"
	urlMSI    = "https://github.com/rustdesk/rustdesk/releases/download/1.4.9/rustdesk-1.4.9-x86_64.msi"
	sha256MSI = "c87d2f4cef2a5acd6003b6507dcfbf5d5168a256db082cd90b54d35193224aaa"

	nombreServicio = "RustDesk"
	// Un MSI de ~26 MB: con internet lento puede tardar.
	tiempoDescarga = 15 * time.Minute
)

// RutaExe es donde lo deja el MSI oficial.
func RutaExe() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "RustDesk", "rustdesk.exe")
}

// Instalado dice si RustDesk está en el equipo (lo haya puesto el agente o no).
func Instalado() bool {
	_, err := os.Stat(RutaExe())
	return err == nil
}

func rutaLogInstalacion(dir string) string { return filepath.Join(dir, "rustdesk-instalacion.log") }

// InstaladoPorAgente: el log del MSI solo lo deja Instalar. Sirve para
// reconocer un RustDesk que puso el agente aunque la instalación no haya
// terminado de configurarlo (v0.6.0 lo dejaba sin servicio y lo creía ajeno).
func InstaladoPorAgente(dir string) bool {
	_, err := os.Stat(rutaLogInstalacion(dir))
	return err == nil && Instalado()
}

// Instalar descarga el MSI oficial a dir, verifica el SHA-256, lo instala en
// silencio (sin accesos directos ni impresora virtual) y lo deja APAGADO.
// progreso puede ser nil.
func Instalar(dir string, progreso func(bajado, total int64)) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	msi := filepath.Join(dir, "rustdesk-"+Version+".msi")
	defer os.Remove(msi)
	if err := descargar(msi, progreso); err != nil {
		return fmt.Errorf("descargar RustDesk: %w", err)
	}
	logMSI := rutaLogInstalacion(dir)
	cmd := exec.Command("msiexec", "/i", msi, "/qn", "/norestart",
		"CREATESTARTMENUSHORTCUTS=N", "CREATEDESKTOPSHORTCUTS=N", "INSTALLPRINTER=N",
		"/l*v", logMSI)
	oculto(cmd)
	if err := cmd.Run(); err != nil {
		// 3010 = instalado, pide reiniciar (no hace falta para usarlo).
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 3010 {
			return fmt.Errorf("el instalador de RustDesk falló (%v); detalle en %s", err, logMSI)
		}
	}
	if !Instalado() {
		return fmt.Errorf("el instalador terminó pero no está %s; detalle en %s", RutaExe(), logMSI)
	}
	return Preparar()
}

// Preparar deja un RustDesk instalado por el agente como lo necesitamos: con
// su servicio de Windows (el MSI en silencio NO lo crea), en inicio manual,
// sin ícono al iniciar sesión y APAGADO hasta que lo pidan.
func Preparar() error {
	if err := asegurarServicio(); err != nil {
		return err
	}
	if err := ponerInicioManual(); err != nil {
		return err
	}
	quitarBandejaAlIniciar()
	return Apagar()
}

// asegurarServicio crea el servicio de RustDesk si no existe, como hace el
// script oficial de despliegue (`rustdesk.exe --install-service`).
func asegurarServicio() error {
	if servicioExiste() {
		return nil
	}
	ctx, cancelar := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelar()
	cmd := exec.CommandContext(ctx, RutaExe(), "--install-service")
	oculto(cmd)
	_ = cmd.Run() // no informa nada útil: se mira si el servicio apareció
	for fin := time.Now().Add(60 * time.Second); time.Now().Before(fin); time.Sleep(time.Second) {
		if servicioExiste() {
			return nil
		}
	}
	return errors.New("RustDesk no creó su servicio de Windows (--install-service)")
}

func servicioExiste() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(nombreServicio)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

func descargar(destino string, progreso func(bajado, total int64)) error {
	ctx, cancelar := context.WithTimeout(context.Background(), tiempoDescarga)
	defer cancelar()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlMSI, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub respondió %d", res.StatusCode)
	}
	f, err := os.Create(destino)
	if err != nil {
		return err
	}
	h := sha256.New()
	var src io.Reader = res.Body
	if progreso != nil {
		src = &lectorConProgreso{r: res.Body, total: res.ContentLength, fn: progreso}
	}
	_, err = io.Copy(io.MultiWriter(f, h), src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha256MSI {
		return fmt.Errorf("el archivo descargado no es el oficial (SHA-256 %s)", got)
	}
	return nil
}

type lectorConProgreso struct {
	r      io.Reader
	total  int64
	bajado int64
	ultimo time.Time
	fn     func(bajado, total int64)
}

func (l *lectorConProgreso) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.bajado += int64(n)
	if time.Since(l.ultimo) > 500*time.Millisecond || err == io.EOF {
		l.ultimo = time.Now()
		l.fn(l.bajado, l.total)
	}
	return n, err
}

// Desinstalar quita RustDesk (solo se llama si lo instaló el agente).
func Desinstalar() error {
	_ = Apagar()
	if codigo := codigoProductoMSI(); codigo != "" {
		cmd := exec.Command("msiexec", "/x", codigo, "/qn", "/norestart")
		oculto(cmd)
		if err := cmd.Run(); err == nil || !Instalado() {
			return nil
		}
	}
	if !Instalado() {
		return nil
	}
	cmd := exec.Command(RutaExe(), "--uninstall")
	oculto(cmd)
	return cmd.Run()
}

// codigoProductoMSI busca el {GUID} con el que el MSI de RustDesk se registró.
func codigoProductoMSI() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return ""
	}
	defer k.Close()
	nombres, _ := k.ReadSubKeyNames(-1)
	for _, n := range nombres {
		if !strings.HasPrefix(n, "{") {
			continue
		}
		sk, err := registry.OpenKey(k, n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		nombre, _, _ := sk.GetStringValue("DisplayName")
		sk.Close()
		if strings.EqualFold(nombre, "RustDesk") {
			return n
		}
	}
	return ""
}

// ── encender / apagar ───────────────────────────────────────────────────

// Servidor de RustDesk. Vacío = el servidor público de RustDesk.
type Servidor struct {
	Host string `json:"host"`
	Key  string `json:"key"`
}

// Configurar deja RustDesk como lo necesitamos: el cliente acepta cada
// conexión con un clic y se usa el servidor indicado. Se llama con el
// servicio APAGADO: RustDesk no está escuchando y las opciones van directo a
// su archivo de configuración, que lee al arrancar.
func Configurar(s Servidor) error {
	opciones := [][2]string{
		{"approve-mode", "click"},
		{"custom-rendezvous-server", s.Host},
		{"relay-server", s.Host},
		{"key", s.Key},
	}
	for _, o := range opciones {
		if _, err := rustdesk(20*time.Second, "--option", o[0], o[1]); err != nil {
			return fmt.Errorf("opción %s: %w", o[0], err)
		}
	}
	return nil
}

// Encender arranca el servicio de RustDesk y devuelve el ID del equipo (el
// número que el técnico usa para conectarse).
func Encender() (string, error) {
	m, s, err := abrirServicio()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	if st.State != svc.Running {
		if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return "", fmt.Errorf("no arrancó el servicio de RustDesk: %w", err)
		}
		if !esperarEstado(s, svc.Running, 30*time.Second) {
			return "", errors.New("el servicio de RustDesk no llegó a arrancar")
		}
	}
	// Recién arrancado, RustDesk tarda unos segundos en tener su ID listo.
	var ultimoErr error
	for range 15 {
		id, err := LeerID()
		if err == nil {
			return id, nil
		}
		ultimoErr = err
		time.Sleep(2 * time.Second)
	}
	return "", ultimoErr
}

var reID = regexp.MustCompile(`^\d{6,12}$`)

// LeerID pide el ID a RustDesk.
func LeerID() (string, error) {
	out, err := rustdesk(20*time.Second, "--get-id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if !reID.MatchString(id) {
		return "", fmt.Errorf("RustDesk no devolvió un ID válido (%q)", id)
	}
	return id, nil
}

// Apagar detiene el servicio de RustDesk y cierra lo que quede abierto (la
// ventana de conexión, la bandeja): mientras está apagado nadie puede conectarse.
func Apagar() error {
	m, s, err := abrirServicio()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		esperarEstado(s, svc.Stopped, 20*time.Second)
	}
	cmd := exec.Command("taskkill", "/F", "/T", "/IM", "rustdesk.exe")
	oculto(cmd)
	_ = cmd.Run() // sin procesos = código 128: no es un error
	return nil
}

// Encendido dice si el servicio de RustDesk está corriendo.
func Encendido() bool {
	m, s, err := abrirServicio()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	defer s.Close()
	st, err := s.Query()
	return err == nil && st.State == svc.Running
}

// SesionActiva dice si hay un técnico conectado ahora: mientras dura la
// conexión, RustDesk tiene abierta la ventana de conexión (`--cm`).
func SesionActiva() bool {
	var ps []struct{ CommandLine *string }
	if err := wmi.Query("SELECT CommandLine FROM Win32_Process WHERE Name = 'rustdesk.exe'", &ps); err != nil {
		return false
	}
	for _, p := range ps {
		if p.CommandLine != nil && strings.Contains(*p.CommandLine, "--cm") {
			return true
		}
	}
	return false
}

func ponerInicioManual() error {
	m, s, err := abrirServicio()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	if c.StartType == mgr.StartManual {
		return nil
	}
	c.StartType = mgr.StartManual
	c.DelayedAutoStart = false
	return s.UpdateConfig(c)
}

// AsegurarInicioManual: si alguien lo volvió a poner en automático (p. ej.
// una actualización de RustDesk), arrancaría con Windows sin que nadie lo pida.
func AsegurarInicioManual() error { return ponerInicioManual() }

// quitarBandejaAlIniciar: el MSI agrega el ícono de RustDesk al inicio de
// sesión. Apagado no sirve y confunde al cliente.
func quitarBandejaAlIniciar() {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	_ = os.Remove(filepath.Join(base, `Microsoft\Windows\Start Menu\Programs\Startup\RustDesk Tray.lnk`))
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	nombres, _ := k.ReadValueNames(-1)
	for _, n := range nombres {
		v, _, err := k.GetStringValue(n)
		if err == nil && strings.Contains(strings.ToLower(v), "rustdesk.exe") && strings.Contains(v, "--tray") {
			_ = k.DeleteValue(n)
		}
	}
}

func abrirServicio() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	s, err := m.OpenService(nombreServicio)
	if err != nil {
		m.Disconnect()
		return nil, nil, fmt.Errorf("RustDesk no tiene su servicio instalado: %w", err)
	}
	return m, s, nil
}

func esperarEstado(s *mgr.Service, estado svc.State, max time.Duration) bool {
	for fin := time.Now().Add(max); time.Now().Before(fin); time.Sleep(500 * time.Millisecond) {
		if st, err := s.Query(); err == nil && st.State == estado {
			return true
		}
	}
	return false
}

// rustdesk corre el exe de RustDesk con argumentos de línea de comandos y
// devuelve lo que imprime.
func rustdesk(max time.Duration, args ...string) (string, error) {
	ctx, cancelar := context.WithTimeout(context.Background(), max)
	defer cancelar()
	cmd := exec.CommandContext(ctx, RutaExe(), args...)
	oculto(cmd)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("RustDesk no respondió (%s)", strings.Join(args, " "))
	}
	if err != nil {
		return "", err
	}
	if s := strings.TrimSpace(string(out)); strings.Contains(s, "privileges required") {
		return "", errors.New(s)
	}
	return string(out), nil
}

func oculto(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
