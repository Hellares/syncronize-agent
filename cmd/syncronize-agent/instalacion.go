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
	"github.com/Hellares/syncronize-agent/internal/remoto"
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
		// Windows puede tardar un poco en soltar el .exe del servicio detenido.
		var errCopia error
		for range 15 {
			if errCopia = copiarArchivo(propio, rutaInstalada()); errCopia == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if errCopia != nil {
			// Nunca dejar el servicio apagado: arranca con la versión anterior.
			if instalado {
				_ = s.Start()
			}
			return fmt.Errorf("no se pudo copiar el agente a %s (sigue la versión anterior): %w", dirInstalacion(), errCopia)
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

// detenerSiCorre para el servicio (si corre) y dice si lo paró.
func detenerSiCorre() bool {
	s := nuevoServicio()
	if st, err := s.Status(); err != nil || st != service.StatusRunning {
		return false
	}
	_ = s.Stop()
	esperarDetenido(s)
	return true
}

func esperarDetenido(s service.Service) {
	for range 60 {
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
	fmt.Println("El soporte remoto es aparte: se pregunta y es opcional.")
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
		fmt.Println("  3) Soporte remoto: autorizar o quitar")
		fmt.Print("\nElige 1, 2 o 3 [1]: ")
		resp, _ := lector.ReadString('\n')
		if r := strings.TrimSpace(resp); r != "2" {
			// Los vínculos de antes del soporte remoto: se les pregunta una vez.
			for _, v := range cfg.Vinculos {
				if r == "3" || v.SoporteRemoto == nil {
					preguntarSoporte(lector, v)
				}
			}
			prepararSoporte(cfg)
			if err := config.Guardar(cfg); err != nil {
				fmt.Println("\nNo se pudo guardar:", err)
				pausa()
				return
			}
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
		if _, err := vincularConCodigo(codigo, servidorPorDefecto(), lector, nil); err != nil {
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

// preguntarSoporte pide al cliente (no al técnico) que decida si ese servicio
// técnico le puede dar soporte remoto. Por defecto, no.
func preguntarSoporte(lector *bufio.Reader, v *config.Vinculo) {
	fmt.Println()
	fmt.Println("----------------------------------------------")
	fmt.Printf("SOPORTE REMOTO con %s\n", v.Nombre())
	fmt.Println("----------------------------------------------")
	fmt.Println("Permite que el técnico vea y use esta pantalla para ayudarte.")
	fmt.Println("  - Cada vez que quiera conectarse aparece una ventana y TÚ")
	fmt.Println("    decides si aceptas. Sin tu clic no puede entrar.")
	fmt.Println("  - Solo se enciende cuando el técnico lo pide y se apaga al terminar.")
	fmt.Println("  - Usa RustDesk (programa de código abierto), que se instala aparte.")
	actual := "n"
	if v.SoporteAutorizado() {
		actual = "s"
	}
	fmt.Printf("\n¿Lo autorizas? Escribe s (sí) o n (no) [%s]: ", actual)
	resp, _ := lector.ReadString('\n')
	r := strings.ToLower(strings.TrimSpace(resp))
	if r == "" {
		r = actual
	}
	si := r == "s" || r == "si" || r == "sí"
	v.SoporteRemoto = &si
	if si {
		fmt.Println("Autorizado.")
	} else {
		fmt.Println("No autorizado: solo monitoreo.")
	}
}

// prepararSoporte instala RustDesk ya (con el cliente mirando) si alguien lo
// autorizó. Si falla, el servicio lo vuelve a intentar cuando el técnico lo pida.
// Si ya nadie lo autoriza, el servicio lo desinstala al arrancar. Devuelve una
// nota para el resumen del instalador ("" si no hay nada que decir).
func prepararSoporte(cfg *config.Config) string {
	if cfg.ConSoporte() == 0 {
		if cfg.RustDeskPropio && remoto.Instalado() {
			fmt.Println("\nSe quitará el soporte remoto (RustDesk) de este equipo.")
			return "Se quitó el soporte remoto de este equipo."
		}
		return ""
	}
	if remoto.InstaladoPorAgente(config.Dir()) {
		cfg.RustDeskPropio = true
	}
	if remoto.Instalado() {
		if !cfg.RustDeskPropio {
			fmt.Println("\nEste equipo ya tenía RustDesk: se usará ese, sin cambiar su configuración.")
			return "Soporte remoto: se usará el RustDesk que ya tenía este equipo."
		}
		// Lo puso el agente: se completa lo que falte (p. ej. el servicio que
		// v0.6.0 no creaba).
		if err := remoto.Preparar(); err != nil {
			fmt.Println("\nNo se pudo preparar el soporte remoto:", err)
			fmt.Println("Se volverá a intentar cuando el técnico lo pida.")
			return "Soporte remoto: se terminará de preparar cuando el técnico lo pida."
		}
		return "Soporte remoto listo. Queda apagado hasta que el técnico lo pida."
	}
	fmt.Printf("\nInstalando el soporte remoto (RustDesk %s, ~26 MB)...\n", remoto.Version)
	err := remoto.Instalar(config.Dir(), func(bajado, total int64) {
		if total > 0 {
			fmt.Printf("\r  descargando: %3d %%", bajado*100/total)
		}
	})
	fmt.Println()
	if remoto.Instalado() {
		// Aunque falle un paso posterior, RustDesk lo puso el agente.
		cfg.RustDeskPropio = true
	}
	if err != nil {
		fmt.Println("No se pudo instalar ahora:", err)
		fmt.Println("Se volverá a intentar solo cuando el técnico pida soporte.")
		return "Soporte remoto: no se pudo instalar ahora; se volverá a intentar cuando el técnico lo pida."
	}
	cfg.RustDeskPropio = true
	fmt.Println("Soporte remoto instalado. Queda APAGADO hasta que el técnico lo pida.")
	return "Soporte remoto instalado. Queda apagado hasta que el técnico lo pida."
}

func pausa() {
	fmt.Print("\nPresiona Enter para salir...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
