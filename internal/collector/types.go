// Package collector lee el estado del equipo (solo lectura) y lo arma como
// Reporte: el CONTRATO que el agente envía al backend de Syncronize.
//
// Reglas:
//   - Solo datos técnicos. Nunca archivos, fotos, historial, ni contraseñas.
//   - Cada sección es "best effort": si una lectura falla, el resto sigue y
//     el error queda en Reporte.Errores (el backend decide qué mostrar).
//   - Los punteros (*T) significan "no disponible en este equipo" (nil),
//     que NO es lo mismo que cero.
package collector

import "time"

// VersionContrato sube cuando cambia la forma del Reporte (no por datos nuevos opcionales).
const VersionContrato = 1

type Reporte struct {
	Version       int       `json:"version"`
	AgenteVersion string    `json:"agenteVersion"`
	TomadoEn      time.Time `json:"tomadoEn"`

	Equipo     Equipo      `json:"equipo"`
	Sistema    Sistema     `json:"sistema"`
	CPU        CPU         `json:"cpu"`
	Memoria    Memoria     `json:"memoria"`
	Discos     []Disco     `json:"discos"`
	Volumenes  []Volumen   `json:"volumenes"`
	Bateria    *Bateria    `json:"bateria"` // nil = no tiene batería (PC de escritorio)
	Red        []Adaptador `json:"red"`
	Impresoras []Impresora `json:"impresoras"`
	Seguridad  Seguridad   `json:"seguridad"`

	// Lecturas que fallaron (sección: mensaje). No detienen el reporte.
	Errores []string `json:"errores"`
}

type Equipo struct {
	Hostname    string `json:"hostname"`
	Fabricante  string `json:"fabricante"`
	Modelo      string `json:"modelo"`
	NumeroSerie string `json:"numeroSerie"`
	UUID        string `json:"uuid"`
	// LAPTOP | ESCRITORIO | OTRO (de Win32_SystemEnclosure y de si hay batería).
	Tipo          string `json:"tipo"`
	UsuarioActual string `json:"usuarioActual"`
}

type Sistema struct {
	Nombre           string     `json:"nombre"`  // "Microsoft Windows 11 Pro"
	Version          string     `json:"version"` // "10.0.22631"
	Build            string     `json:"build"`
	Arquitectura     string     `json:"arquitectura"`
	FechaInstalacion *time.Time `json:"fechaInstalacion"`
	UltimoReinicio   *time.Time `json:"ultimoReinicio"`
	UptimeSegundos   uint64     `json:"uptimeSegundos"`
	// Fecha del último parche instalado (Win32_QuickFixEngineering).
	UltimaActualizacion *time.Time `json:"ultimaActualizacion"`
}

type CPU struct {
	Modelo        string  `json:"modelo"`
	Nucleos       int     `json:"nucleos"`
	Hilos         int     `json:"hilos"`
	MHz           float64 `json:"mhz"`
	UsoPorcentaje float64 `json:"usoPorcentaje"`
}

type Memoria struct {
	TotalBytes      uint64          `json:"totalBytes"`
	DisponibleBytes uint64          `json:"disponibleBytes"`
	UsoPorcentaje   float64         `json:"usoPorcentaje"`
	Modulos         []ModuloMemoria `json:"modulos"`
}

type ModuloMemoria struct {
	CapacidadBytes uint64 `json:"capacidadBytes"`
	VelocidadMHz   uint32 `json:"velocidadMhz"`
	Fabricante     string `json:"fabricante"`
	NumeroParte    string `json:"numeroParte"`
	Ranura         string `json:"ranura"`
}

// Disco físico (de MSFT_PhysicalDisk + contadores de confiabilidad).
type Disco struct {
	Modelo      string `json:"modelo"`
	NumeroSerie string `json:"numeroSerie"`
	Tipo        string `json:"tipo"` // SSD | HDD | DESCONOCIDO
	Bus         string `json:"bus"`  // NVME | SATA | USB | SAS | OTRO
	TamanoBytes uint64 `json:"tamanoBytes"`
	// SALUDABLE | ADVERTENCIA | CRITICO | DESCONOCIDO (HealthStatus de Windows).
	Salud string `json:"salud"`
	// Desde los contadores de confiabilidad (solo si Windows los expone).
	TemperaturaC       *int    `json:"temperaturaC"`
	DesgastePorcentaje *int    `json:"desgastePorcentaje"` // SSD: 0 = nuevo, 100 = agotado
	HorasEncendido     *uint64 `json:"horasEncendido"`
	ErroresLectura     *uint64 `json:"erroresLectura"`
}

// Volumen lógico (C:, D:...).
type Volumen struct {
	Unidad          string  `json:"unidad"`
	SistemaArchivos string  `json:"sistemaArchivos"`
	TotalBytes      uint64  `json:"totalBytes"`
	LibreBytes      uint64  `json:"libreBytes"`
	UsoPorcentaje   float64 `json:"usoPorcentaje"`
}

type Bateria struct {
	CapacidadDisenoMWh *uint32 `json:"capacidadDisenoMWh"`
	CapacidadMaximaMWh *uint32 `json:"capacidadMaximaMWh"`
	// Capacidad máxima actual / capacidad de diseño, en %.
	SaludPorcentaje *float64 `json:"saludPorcentaje"`
	CargaPorcentaje *uint16  `json:"cargaPorcentaje"`
	Ciclos          *uint32  `json:"ciclos"`
	Estado          string   `json:"estado"` // CARGANDO | DESCARGANDO | CONECTADA | DESCONOCIDO
}

type Adaptador struct {
	Nombre        string   `json:"nombre"`
	MAC           string   `json:"mac"`
	IPs           []string `json:"ips"`
	Gateway       []string `json:"gateway"`
	DNS           []string `json:"dns"`
	VelocidadMbps uint64   `json:"velocidadMbps"`
	Conectado     bool     `json:"conectado"`
	Inalambrico   bool     `json:"inalambrico"`
}

type Impresora struct {
	Nombre             string `json:"nombre"`
	Driver             string `json:"driver"`
	Puerto             string `json:"puerto"`
	EnRed              bool   `json:"enRed"`
	Predeterminada     bool   `json:"predeterminada"`
	DesconectadaManual bool   `json:"desconectadaManual"` // "Usar impresora sin conexión"
	// Estado legible desde PrinterStatus / DetectedErrorState de Windows.
	Estado string `json:"estado"`
	Error  string `json:"error"`

	// Lo que contesta la impresora misma (solo las de red: IPP y SNMP).
	// Vacío si no es de red o no respondió.
	IP              string   `json:"ip,omitempty"`
	Fabricante      string   `json:"fabricante,omitempty"` // "EPSON"
	Modelo          string   `json:"modelo,omitempty"`     // "EPSON L4260 Series"
	Serie           string   `json:"serie,omitempty"`
	ContadorPaginas *int64   `json:"contadorPaginas,omitempty"`
	Firmware        string   `json:"firmware,omitempty"`
	Tintas          []Tinta  `json:"tintas,omitempty"`
	Alertas         []string `json:"alertas,omitempty"` // printer-state-reasons de IPP ("media-jam"...)
	// La misma impresora instalada por USB y por WiFi: la de USB copia los
	// datos de la de red y acá dice de cuál (el backend no duplica alertas).
	DatosDe string `json:"datosDe,omitempty"`
}

// Tinta o tóner según la impresora (IPP marker-*).
type Tinta struct {
	Nombre string `json:"nombre"` // "Black ink"
	Color  string `json:"color"`  // "#000000"
	Tipo   string `json:"tipo"`   // ink | toner | ...
	// Nivel en % (0-100). -1 = la impresora no lo sabe, -2 = desconocido,
	// -3 = "queda algo" (convención IPP/Printer MIB).
	Nivel int `json:"nivel"`
	// Desde este % la impresora lo considera bajo.
	Bajo int `json:"bajo"`
}

type Seguridad struct {
	// Antivirus registrado en el Centro de seguridad de Windows (puede no ser Defender).
	AntivirusNombre      string  `json:"antivirusNombre"`
	AntivirusActivo      *bool   `json:"antivirusActivo"`
	ProteccionTiempoReal *bool   `json:"proteccionTiempoReal"`
	AntiguedadFirmasDias *uint32 `json:"antiguedadFirmasDias"`
}
