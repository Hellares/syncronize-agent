package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"

	"github.com/Hellares/syncronize-agent/internal/config"
)

// ServidorPorDefecto lo fija el build (`build.ps1 -Beta` apunta a beta).
// Vacío = producción.
var ServidorPorDefecto = ""

func servidorPorDefecto() string {
	if ServidorPorDefecto != "" {
		return ServidorPorDefecto
	}
	return config.ServidorProd
}

// dirInstalacion: el servicio corre SIEMPRE desde acá, no desde donde el
// técnico dejó el .exe (Descargas, un USB...): si lo borra, el servicio sigue.
func dirInstalacion() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "Syncronize", "Agent")
}

func rutaInstalada() string { return filepath.Join(dirInstalacion(), "syncronize-agent.exe") }

// instalarYArrancar copia el ejecutable a Program Files (si no corre ya desde
// ahí), registra el servicio si no existe y lo (re)arranca.
func instalarYArrancar() error {
	s := nuevoServicio()
	_, err := s.Status()
	instalado := !errors.Is(err, service.ErrNotInstalled)
	if instalado {
		_ = s.Stop() // suelta el .exe para poder reemplazarlo
		esperarDetenido(s)
	}

	propio, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(propio), filepath.Clean(rutaInstalada())) {
		if err := copiarArchivo(propio, rutaInstalada()); err != nil {
			return fmt.Errorf("no se pudo copiar el agente a %s: %w", dirInstalacion(), err)
		}
	}
	if !instalado {
		if err := s.Install(); err != nil {
			return fmt.Errorf("no se pudo instalar el servicio: %w", err)
		}
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("el servicio no arrancó: %w", err)
	}
	return nil
}

func esperarDetenido(s service.Service) {
	for i := 0; i < 20; i++ {
		if st, err := s.Status(); err != nil || st != service.StatusRunning {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func copiarArchivo(origen, destino string) error {
	if err := os.MkdirAll(filepath.Dir(destino), 0o755); err != nil {
		return err
	}
	in, err := os.Open(origen)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := destino + ".nuevo"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, destino)
}

// borrarInstalacion quita la carpeta de Program Files. Si el que corre es el
// .exe instalado, Windows no deja borrarlo en uso: lo borra un cmd aparte
// apenas este proceso termina.
func borrarInstalacion() {
	propio, _ := os.Executable()
	if !strings.EqualFold(filepath.Clean(propio), filepath.Clean(rutaInstalada())) {
		_ = os.RemoveAll(dirInstalacion())
		return
	}
	cmd := exec.Command("cmd", "/c", "ping 127.0.0.1 -n 3 >nul & rmdir /s /q \""+dirInstalacion()+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
	_ = cmd.Start()
}

// ── asistente (doble clic) ──────────────────────────────────────────────

// asistente es lo que pasa con doble clic sobre el .exe: pide permisos de
// administrador, el código, y vincula. Sin consola ni comandos.
func asistente() {
	if !windows.GetCurrentProcessToken().IsElevated() {
		// Se relanza elevado (aparece el aviso de Windows) y esta ventana se cierra.
		propio, _ := os.Executable()
		verbo, _ := windows.UTF16PtrFromString("runas")
		exe, _ := windows.UTF16PtrFromString(propio)
		if err := windows.ShellExecute(0, verbo, exe, nil, nil, windows.SW_NORMAL); err != nil {
			fmt.Println("Hace falta aceptar el permiso de administrador para instalar el agente.")
			pausa()
		}
		return
	}

	fmt.Println("==============================================")
	fmt.Println("  Syncronize - Agente de monitoreo  v" + Version)
	fmt.Println("==============================================")
	fmt.Println()
	fmt.Println("Reporta a tu servicio técnico el estado del equipo:")
	fmt.Println("discos, batería, memoria, espacio, antivirus e impresoras.")
	fmt.Println("NO lee archivos, fotos, contraseñas ni historial.")
	fmt.Println()
	if servidorPorDefecto() != config.ServidorProd {
		fmt.Println("(servidor de PRUEBAS: " + servidorPorDefecto() + ")")
		fmt.Println()
	}

	lector := bufio.NewReader(os.Stdin)

	// Ya vinculado (con uno o más servicios técnicos): se puede ACTUALIZAR el
	// agente (sin código) o AGREGAR otro servicio técnico (con su código). Los
	// vínculos que ya había siguen: cada técnico ve solo lo suyo.
	cfg, errCfg := config.Cargar()
	if errCfg == nil {
		fmt.Println("Este equipo ya está vinculado con:")
		for _, v := range cfg.Vinculos {
			desde := ""
			if !v.VinculadoEn.IsZero() {
				desde = " (desde " + v.VinculadoEn.Local().Format("02/01/2006") + ")"
			}
			otro := ""
			if !strings.EqualFold(strings.TrimRight(v.Servidor, "/"), strings.TrimRight(servidorPorDefecto(), "/")) {
				otro = " [" + nombreServidor(v.Servidor) + "]"
			}
			fmt.Printf("  - %s%s%s\n", v.Nombre(), desde, otro)
		}
		fmt.Println()
		fmt.Printf("  1) Actualizar el agente a la versión %s\n", Version)
		fmt.Println("  2) Agregar OTRO servicio técnico (necesita su código)")
		fmt.Print("\nElige 1 o 2 [1]: ")
		resp, _ := lector.ReadString('\n')
		if r := strings.TrimSpace(resp); r != "2" {
			if err := instalarYArrancar(); err != nil {
				fmt.Println("\nNo se pudo actualizar:", err)
			} else {
				fmt.Println("\nListo: agente actualizado y corriendo.")
			}
			pausa()
			return
		}
		fmt.Println()
	}

	for intento := 0; intento < 3; intento++ {
		fmt.Print("Código de vinculación (lo da el técnico): ")
		linea, _ := lector.ReadString('\n')
		codigo := strings.ToUpper(strings.TrimSpace(linea))
		if codigo == "" {
			continue
		}
		fmt.Println()
		if err := vincularConCodigo(codigo, servidorPorDefecto()); err != nil {
			fmt.Println()
			fmt.Println("No se pudo vincular:", err)
			fmt.Println()
			continue
		}
		fmt.Println()
		fmt.Println("Listo. Ya puedes cerrar esta ventana.")
		pausa()
		return
	}
	pausa()
}

// nombreServidor: "PRODUCCIÓN" / "BETA (pruebas)" / la URL tal cual.
func nombreServidor(url string) string {
	switch strings.TrimRight(url, "/") {
	case config.ServidorProd:
		return "PRODUCCIÓN"
	case config.ServidorBeta:
		return "BETA (pruebas)"
	}
	return url
}

func pausa() {
	fmt.Print("\nPresiona Enter para salir...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
