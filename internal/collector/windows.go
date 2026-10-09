//go:build windows

package collector

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/yusufpapurcu/wmi"
	"golang.org/x/sys/windows/registry"
)

const (
	nsStorage   = `root\Microsoft\Windows\Storage`
	nsWMI       = `root\wmi`
	nsDefender  = `root\Microsoft\Windows\Defender`
	nsSecCenter = `root\SecurityCenter2`
)

// ── Equipo ──────────────────────────────────────────────────────────────

type win32ComputerSystem struct {
	Manufacturer string
	Model        string
	UserName     string
}
type win32BIOS struct{ SerialNumber string }
type win32BaseBoard struct{ SerialNumber string }
type win32ComputerSystemProduct struct{ UUID string }

// WMI devuelve el arreglo como int32 (con uint16 el reflect entra en pánico).
type win32SystemEnclosure struct{ ChassisTypes []int32 }

// Textos de relleno que dejan los fabricantes de placas (PCs armadas).
var seriesDeRelleno = []string{"default string", "to be filled by o.e.m.", "system serial number", "none", "0", "n/a", "not specified", "chassis serial number", "base board serial number"}

func serieValida(s string) string {
	s = limpiar(s)
	for _, r := range seriesDeRelleno {
		if strings.EqualFold(s, r) {
			return ""
		}
	}
	return s
}

func leerEquipo(e *Equipo) error {
	var cs []win32ComputerSystem
	if err := wmi.Query("SELECT Manufacturer, Model, UserName FROM Win32_ComputerSystem", &cs); err != nil {
		return err
	}
	if len(cs) > 0 {
		e.Fabricante = limpiar(cs[0].Manufacturer)
		e.Modelo = limpiar(cs[0].Model)
		e.UsuarioActual = cs[0].UserName
	}
	var bios []win32BIOS
	if err := wmi.Query("SELECT SerialNumber FROM Win32_BIOS", &bios); err == nil && len(bios) > 0 {
		e.NumeroSerie = serieValida(bios[0].SerialNumber)
	}
	if e.NumeroSerie == "" {
		// PC armada: la serie que vale es la de la placa madre.
		var bb []win32BaseBoard
		if err := wmi.Query("SELECT SerialNumber FROM Win32_BaseBoard", &bb); err == nil && len(bb) > 0 {
			e.NumeroSerie = serieValida(bb[0].SerialNumber)
		}
	}
	var prod []win32ComputerSystemProduct
	if err := wmi.Query("SELECT UUID FROM Win32_ComputerSystemProduct", &prod); err == nil && len(prod) > 0 {
		e.UUID = prod[0].UUID
	}
	e.Tipo = "OTRO"
	var enc []win32SystemEnclosure
	if err := wmi.Query("SELECT ChassisTypes FROM Win32_SystemEnclosure", &enc); err == nil && len(enc) > 0 {
		e.Tipo = tipoDeGabinete(enc[0].ChassisTypes)
	}
	return nil
}

// Códigos SMBIOS de gabinete.
func tipoDeGabinete(tipos []int32) string {
	for _, t := range tipos {
		switch t {
		case 8, 9, 10, 11, 12, 14, 18, 21, 30, 31, 32:
			return "LAPTOP"
		case 3, 4, 5, 6, 7, 13, 15, 16, 24, 35, 36:
			return "ESCRITORIO"
		}
	}
	return "OTRO"
}

// ── Sistema ─────────────────────────────────────────────────────────────

type win32OperatingSystem struct {
	Caption        string
	Version        string
	BuildNumber    string
	OSArchitecture string
	InstallDate    time.Time
	LastBootUpTime time.Time
}
type win32QuickFix struct{ InstalledOn string }

func leerSistema(s *Sistema) error {
	var so []win32OperatingSystem
	if err := wmi.Query("SELECT Caption, Version, BuildNumber, OSArchitecture, InstallDate, LastBootUpTime FROM Win32_OperatingSystem", &so); err != nil {
		return err
	}
	if len(so) > 0 {
		s.Nombre = limpiar(so[0].Caption)
		s.Version = so[0].Version
		s.Build = so[0].BuildNumber
		s.Arquitectura = so[0].OSArchitecture
		s.FechaInstalacion = fechaOpcional(so[0].InstallDate)
		s.UltimoReinicio = fechaOpcional(so[0].LastBootUpTime)
	}
	if up, err := host.Uptime(); err == nil {
		s.UptimeSegundos = up
	}
	// Último parche: InstalledOn viene como "M/D/YYYY" (o vacío en algunos).
	var qfe []win32QuickFix
	if err := wmi.Query("SELECT InstalledOn FROM Win32_QuickFixEngineering", &qfe); err == nil {
		var ultima time.Time
		for _, q := range qfe {
			if t, err := time.Parse("1/2/2006", strings.TrimSpace(q.InstalledOn)); err == nil && t.After(ultima) {
				ultima = t
			}
		}
		s.UltimaActualizacion = fechaOpcional(ultima)
	}
	return nil
}

// ── CPU ─────────────────────────────────────────────────────────────────

type win32Processor struct {
	Name                      string
	NumberOfCores             uint32
	NumberOfLogicalProcessors uint32
	MaxClockSpeed             uint32
}

func leerCPU(c *CPU) error {
	var ps []win32Processor
	if err := wmi.Query("SELECT Name, NumberOfCores, NumberOfLogicalProcessors, MaxClockSpeed FROM Win32_Processor", &ps); err != nil {
		return err
	}
	for i, p := range ps {
		if i == 0 {
			c.Modelo = limpiar(p.Name)
			c.MHz = float64(p.MaxClockSpeed)
		}
		c.Nucleos += int(p.NumberOfCores)
		c.Hilos += int(p.NumberOfLogicalProcessors)
	}
	// Uso promedio de 1 segundo.
	if pct, err := cpu.Percent(time.Second, false); err == nil && len(pct) > 0 {
		c.UsoPorcentaje = redondear(pct[0])
	}
	return nil
}

// ── Memoria ─────────────────────────────────────────────────────────────

type win32PhysicalMemory struct {
	Capacity      uint64
	Speed         uint32
	Manufacturer  string
	PartNumber    string
	BankLabel     string
	DeviceLocator string
}

func leerMemoria(m *Memoria) error {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return err
	}
	m.TotalBytes = vm.Total
	m.DisponibleBytes = vm.Available
	m.UsoPorcentaje = redondear(vm.UsedPercent)
	m.Modulos = []ModuloMemoria{}
	var mods []win32PhysicalMemory
	if err := wmi.Query("SELECT Capacity, Speed, Manufacturer, PartNumber, BankLabel, DeviceLocator FROM Win32_PhysicalMemory", &mods); err == nil {
		for _, x := range mods {
			m.Modulos = append(m.Modulos, ModuloMemoria{
				CapacidadBytes: x.Capacity,
				VelocidadMHz:   x.Speed,
				Fabricante:     limpiar(x.Manufacturer),
				NumeroParte:    limpiar(x.PartNumber),
				Ranura:         strings.TrimSpace(limpiar(x.BankLabel) + " " + limpiar(x.DeviceLocator)),
			})
		}
	}
	return nil
}

// ── Discos físicos ──────────────────────────────────────────────────────

type msftPhysicalDisk struct {
	ObjectId     string
	DeviceId     string
	FriendlyName string
	SerialNumber string
	MediaType    uint16
	BusType      uint16
	Size         uint64
	HealthStatus uint16
}

type msftReliability struct {
	DeviceId        string
	Temperature     uint8
	Wear            uint8
	PowerOnHours    uint32
	ReadErrorsTotal uint64
}

func leerDiscos() ([]Disco, error) {
	var pds []msftPhysicalDisk
	if err := wmi.QueryNamespace("SELECT ObjectId, DeviceId, FriendlyName, SerialNumber, MediaType, BusType, Size, HealthStatus FROM MSFT_PhysicalDisk", &pds, nsStorage); err != nil {
		return []Disco{}, err
	}
	// Temperatura, desgaste y horas: requieren correr como administrador
	// (el servicio corre como SYSTEM). Si no salen, el motivo va a errores[]
	// y el disco se reporta igual, sin esos datos.
	var notas []string
	porDeviceID := map[string]msftReliability{}
	var todos []msftReliability
	if err := wmi.QueryNamespace("SELECT DeviceId, Temperature, Wear, PowerOnHours, ReadErrorsTotal FROM MSFT_StorageReliabilityCounter", &todos, nsStorage); err != nil {
		notas = append(notas, fmt.Sprintf("contadores: %v", err))
	} else {
		for _, rc := range todos {
			porDeviceID[rc.DeviceId] = rc
		}
	}
	// Emparejar disco ↔ contadores: 1) la asociación de Windows (lo que hace
	// Get-PhysicalDisk | Get-StorageReliabilityCounter), 2) mismo DeviceId,
	// 3) si hay un solo disco y un solo contador, son pareja.
	contadores := func(p msftPhysicalDisk) (msftReliability, bool) {
		if p.ObjectId != "" {
			var asoc []msftReliability
			q := `ASSOCIATORS OF {MSFT_PhysicalDisk.ObjectId="` + escaparWQL(p.ObjectId) + `"} WHERE AssocClass = MSFT_PhysicalDiskToStorageReliabilityCounter`
			if err := wmi.QueryNamespace(q, &asoc, nsStorage); err == nil && len(asoc) > 0 {
				return asoc[0], true
			} else if err != nil {
				notas = append(notas, fmt.Sprintf("asociación %s: %v", p.DeviceId, err))
			}
		}
		if rc, ok := porDeviceID[p.DeviceId]; ok {
			return rc, true
		}
		if len(pds) == 1 && len(todos) == 1 {
			return todos[0], true
		}
		ids := make([]string, 0, len(todos))
		for _, rc := range todos {
			ids = append(ids, rc.DeviceId)
		}
		notas = append(notas, fmt.Sprintf("disco %s sin contadores (contadores: %v)", p.DeviceId, ids))
		return msftReliability{}, false
	}

	discos := make([]Disco, 0, len(pds))
	for _, p := range pds {
		d := Disco{
			Modelo:      limpiar(p.FriendlyName),
			NumeroSerie: strings.TrimRight(limpiar(p.SerialNumber), ". "),
			Tipo:        map[uint16]string{3: "HDD", 4: "SSD"}[p.MediaType],
			Bus:         busDeDisco(p.BusType),
			TamanoBytes: p.Size,
			Salud:       map[uint16]string{0: "SALUDABLE", 1: "ADVERTENCIA", 2: "CRITICO"}[p.HealthStatus],
		}
		if d.Tipo == "" {
			d.Tipo = "DESCONOCIDO"
			if d.Bus == "NVME" {
				d.Tipo = "SSD"
			}
		}
		if d.Salud == "" {
			d.Salud = "DESCONOCIDO"
		}
		if rc, ok := contadores(p); ok {
			if rc.Temperature > 0 {
				t := int(rc.Temperature)
				d.TemperaturaC = &t
			}
			if d.Tipo == "SSD" {
				w := int(rc.Wear)
				d.DesgastePorcentaje = &w
			}
			if rc.PowerOnHours > 0 {
				h := uint64(rc.PowerOnHours)
				d.HorasEncendido = &h
			}
			re := rc.ReadErrorsTotal
			d.ErroresLectura = &re
		}
		discos = append(discos, d)
	}
	if len(notas) > 0 {
		return discos, fmt.Errorf("%s", strings.Join(notas, "; "))
	}
	return discos, nil
}

// escaparWQL escapa una cadena para ir entre comillas dobles en WQL.
func escaparWQL(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}

func busDeDisco(b uint16) string {
	switch b {
	case 17:
		return "NVME"
	case 11:
		return "SATA"
	case 7:
		return "USB"
	case 10:
		return "SAS"
	default:
		return "OTRO"
	}
}

// ── Volúmenes ───────────────────────────────────────────────────────────

func leerVolumenes() ([]Volumen, error) {
	parts, err := disk.Partitions(false)
	if err != nil {
		return []Volumen{}, err
	}
	vols := []Volumen{}
	for _, p := range parts {
		if p.Fstype == "" || strings.EqualFold(p.Fstype, "CDFS") || strings.EqualFold(p.Fstype, "UDF") {
			continue
		}
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue // lectora sin disco, USB retirado...
		}
		vols = append(vols, Volumen{
			Unidad:          p.Mountpoint,
			SistemaArchivos: p.Fstype,
			TotalBytes:      u.Total,
			LibreBytes:      u.Free,
			UsoPorcentaje:   redondear(u.UsedPercent),
		})
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Unidad < vols[j].Unidad })
	return vols, nil
}

// ── Batería ─────────────────────────────────────────────────────────────

type win32Battery struct {
	EstimatedChargeRemaining uint16
	BatteryStatus            uint16
}
type batteryStaticData struct{ DesignedCapacity uint32 }
type batteryFullCharged struct{ FullChargedCapacity uint32 }
type batteryCycleCount struct{ CycleCount uint32 }

func leerBateria() (*Bateria, error) {
	var bs []win32Battery
	if err := wmi.Query("SELECT EstimatedChargeRemaining, BatteryStatus FROM Win32_Battery", &bs); err != nil {
		return nil, err
	}
	if len(bs) == 0 {
		return nil, nil // sin batería: PC de escritorio
	}
	b := &Bateria{Estado: estadoBateria(bs[0].BatteryStatus)}
	carga := bs[0].EstimatedChargeRemaining
	b.CargaPorcentaje = &carga

	var est []batteryStaticData
	if err := wmi.QueryNamespace("SELECT DesignedCapacity FROM BatteryStaticData", &est, nsWMI); err == nil && len(est) > 0 && est[0].DesignedCapacity > 0 {
		v := est[0].DesignedCapacity
		b.CapacidadDisenoMWh = &v
	}
	var full []batteryFullCharged
	if err := wmi.QueryNamespace("SELECT FullChargedCapacity FROM BatteryFullChargedCapacity", &full, nsWMI); err == nil && len(full) > 0 && full[0].FullChargedCapacity > 0 {
		v := full[0].FullChargedCapacity
		b.CapacidadMaximaMWh = &v
	}
	if b.CapacidadDisenoMWh != nil && b.CapacidadMaximaMWh != nil {
		salud := redondear(float64(*b.CapacidadMaximaMWh) / float64(*b.CapacidadDisenoMWh) * 100)
		if salud > 100 {
			salud = 100 // baterías nuevas a veces reportan algo más que el diseño
		}
		b.SaludPorcentaje = &salud
	}
	var ciclos []batteryCycleCount
	if err := wmi.QueryNamespace("SELECT CycleCount FROM BatteryCycleCount", &ciclos, nsWMI); err == nil && len(ciclos) > 0 && ciclos[0].CycleCount > 0 {
		v := ciclos[0].CycleCount
		b.Ciclos = &v
	}
	return b, nil
}

func estadoBateria(s uint16) string {
	switch s {
	case 1:
		return "DESCARGANDO"
	case 2, 3:
		return "CONECTADA"
	case 6, 7, 8, 9:
		return "CARGANDO"
	default:
		return "DESCONOCIDO"
	}
}

// ── Red ─────────────────────────────────────────────────────────────────

type win32NetConfig struct {
	Index                uint32
	MACAddress           string
	IPAddress            []string
	DefaultIPGateway     []string
	DNSServerSearchOrder []string
}
type win32NetAdapter struct {
	Index               uint32
	Name                string
	NetConnectionID     string
	NetConnectionStatus uint16
	MACAddress          string
	Speed               uint64
	PhysicalAdapter     bool
}

func leerRed() ([]Adaptador, error) {
	var ads []win32NetAdapter
	if err := wmi.Query("SELECT Index, Name, NetConnectionID, NetConnectionStatus, MACAddress, Speed, PhysicalAdapter FROM Win32_NetworkAdapter WHERE PhysicalAdapter = TRUE", &ads); err != nil {
		return []Adaptador{}, err
	}
	var cfgs []win32NetConfig
	_ = wmi.Query("SELECT Index, MACAddress, IPAddress, DefaultIPGateway, DNSServerSearchOrder FROM Win32_NetworkAdapterConfiguration WHERE IPEnabled = TRUE", &cfgs)
	porIndice := map[uint32]win32NetConfig{}
	for _, c := range cfgs {
		porIndice[c.Index] = c
	}

	red := []Adaptador{}
	for _, a := range ads {
		c := porIndice[a.Index]
		nombre := a.NetConnectionID
		if nombre == "" {
			nombre = a.Name
		}
		n := strings.ToLower(a.Name + " " + a.NetConnectionID)
		if strings.Contains(n, "bluetooth") {
			continue // red personal por Bluetooth: no es una conexión del equipo
		}
		conectado := a.NetConnectionStatus == 2
		var velocidad uint64
		if conectado {
			velocidad = a.Speed / 1_000_000 // desconectado, Windows reporta 2^63-1
		}
		mac := c.MACAddress
		if mac == "" {
			mac = a.MACAddress
		}
		red = append(red, Adaptador{
			Nombre:        nombre,
			MAC:           mac,
			IPs:           noNulo(c.IPAddress),
			Gateway:       noNulo(c.DefaultIPGateway),
			DNS:           noNulo(c.DNSServerSearchOrder),
			VelocidadMbps: velocidad,
			Conectado:     conectado,
			Inalambrico:   strings.Contains(n, "wi-fi") || strings.Contains(n, "wireless") || strings.Contains(n, "wlan") || strings.Contains(n, "802.11"),
		})
	}
	return red, nil
}

// ── Impresoras ──────────────────────────────────────────────────────────

type win32Printer struct {
	Name                  string
	DriverName            string
	PortName              string
	Network               bool
	Default               bool
	WorkOffline           bool
	PrinterStatus         uint16
	ExtendedPrinterStatus uint16
	DetectedErrorState    uint16
}

// Impresoras virtuales de Windows: no son equipos.
var impresorasVirtuales = []string{"microsoft print to pdf", "microsoft xps", "onenote", "fax", "pdf"}

func leerImpresoras() ([]Impresora, error) {
	var ps []win32Printer
	if err := wmi.Query("SELECT Name, DriverName, PortName, Network, Default, WorkOffline, PrinterStatus, ExtendedPrinterStatus, DetectedErrorState FROM Win32_Printer", &ps); err != nil {
		return []Impresora{}, err
	}
	out := []Impresora{}
	for _, p := range ps {
		n := strings.ToLower(p.Name + " " + p.DriverName)
		virtual := strings.HasPrefix(strings.ToUpper(p.PortName), "PORTPROMPT") || strings.EqualFold(p.PortName, "nul:")
		for _, v := range impresorasVirtuales {
			if strings.Contains(n, v) {
				virtual = true
			}
		}
		if virtual {
			continue
		}
		out = append(out, Impresora{
			Nombre:             p.Name,
			Driver:             p.DriverName,
			Puerto:             p.PortName,
			EnRed:              p.Network || esPuertoDeRed(p.PortName),
			Predeterminada:     p.Default,
			DesconectadaManual: p.WorkOffline,
			Estado:             estadoImpresora(p.PrinterStatus, p.ExtendedPrinterStatus),
			Error:              map[uint16]string{3: "POCO_PAPEL", 4: "SIN_PAPEL", 5: "POCO_TONER", 6: "SIN_TONER", 7: "PUERTA_ABIERTA", 8: "ATASCO", 9: "SIN_CONEXION", 10: "REQUIERE_SERVICIO", 11: "BANDEJA_LLENA"}[p.DetectedErrorState],
		})
	}
	// Tinta, contador y serie: se le preguntan a la impresora de red misma.
	completarImpresorasRed(out, ipsDeImpresoras(out))
	return out, nil
}

type win32TCPIPPrinterPort struct {
	Name        string
	HostAddress string
}

// ipsDeImpresoras busca la IP (o el host) de cada impresora de red:
//   - puerto TCP/IP estándar: Win32_TCPIPPrinterPort.HostAddress;
//   - puerto WSD (las que Windows agrega solas por WiFi): el registro guarda
//     la URL del dispositivo ("http://192.168.100.4:80/WSD/DEVICE") con el
//     mismo nombre que la impresora;
//   - puerto que ya es una IP ("192.168.1.10", "IP_192.168.1.10").
func ipsDeImpresoras(imps []Impresora) map[int]string {
	out := map[int]string{}
	puertos := map[string]string{}
	var tcp []win32TCPIPPrinterPort
	if err := wmi.Query("SELECT Name, HostAddress FROM Win32_TCPIPPrinterPort", &tcp); err == nil {
		for _, p := range tcp {
			if p.HostAddress != "" {
				puertos[strings.ToLower(p.Name)] = p.HostAddress
			}
		}
	}
	wsd := dispositivosWSD()
	for i, p := range imps {
		puerto := strings.TrimSuffix(p.Puerto, ":")
		switch {
		case puertos[strings.ToLower(p.Puerto)] != "":
			out[i] = puertos[strings.ToLower(p.Puerto)]
		case strings.HasPrefix(strings.ToUpper(puerto), "WSD") && wsd[strings.ToLower(p.Nombre)] != "":
			out[i] = wsd[strings.ToLower(p.Nombre)]
		case net.ParseIP(strings.TrimPrefix(strings.ToUpper(puerto), "IP_")) != nil:
			out[i] = strings.TrimPrefix(strings.ToUpper(puerto), "IP_")
		}
	}
	return out
}

// dispositivosWSD: nombre del dispositivo → host, desde
// HKLM\SYSTEM\CurrentControlSet\Enum\SWD\DAFWSDProvider\*.
func dispositivosWSD() map[string]string {
	out := map[string]string{}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Enum\SWD\DAFWSDProvider`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return out
	}
	defer k.Close()
	subs, _ := k.ReadSubKeyNames(-1)
	for _, s := range subs {
		sk, err := registry.OpenKey(k, s, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		nombre, _, _ := sk.GetStringValue("FriendlyName")
		ubic, _, _ := sk.GetStringValue("LocationInformation")
		sk.Close()
		if u, err := url.Parse(ubic); err == nil && u.Hostname() != "" && nombre != "" {
			out[strings.ToLower(nombre)] = u.Hostname()
		}
	}
	return out
}

// PrinterStatus suele quedar en 1/2 (otro/desconocido) en impresoras
// apagadas o por Bluetooth: ahí manda ExtendedPrinterStatus.
func estadoImpresora(st, ext uint16) string {
	if e, ok := map[uint16]string{3: "LISTA", 4: "IMPRIMIENDO", 5: "CALENTANDO", 6: "DETENIDA", 7: "SIN_CONEXION"}[st]; ok {
		return e
	}
	if e, ok := map[uint16]string{3: "LISTA", 4: "IMPRIMIENDO", 5: "CALENTANDO", 6: "DETENIDA", 7: "SIN_CONEXION", 8: "PAUSADA", 9: "ERROR", 10: "OCUPADA", 11: "NO_DISPONIBLE", 15: "AHORRO_ENERGIA"}[ext]; ok {
		return e
	}
	return "DESCONOCIDO"
}

// Un puerto TCP/IP o WSD es una impresora de red (o WiFi) aunque Windows
// no la marque como "Network".
func esPuertoDeRed(puerto string) bool {
	p := strings.ToUpper(puerto)
	return strings.HasPrefix(p, "IP_") || strings.HasPrefix(p, "WSD") || strings.Count(puerto, ".") == 3
}

// ── Seguridad ───────────────────────────────────────────────────────────

type antiVirusProduct struct {
	DisplayName  string
	ProductState uint32
}
type mpComputerStatus struct {
	RealTimeProtectionEnabled bool
	AntivirusSignatureAge     uint32
}

func leerSeguridad(s *Seguridad) error {
	var avs []antiVirusProduct
	if err := wmi.QueryNamespace("SELECT DisplayName, ProductState FROM AntiVirusProduct", &avs, nsSecCenter); err == nil {
		// Con varios registrados, gana el que esté activo.
		for _, av := range avs {
			activo := (av.ProductState>>12)&0xF == 1
			if s.AntivirusNombre == "" || activo {
				s.AntivirusNombre = av.DisplayName
				s.AntivirusActivo = &activo
			}
			if activo {
				break
			}
		}
	}
	var mp []mpComputerStatus
	if err := wmi.QueryNamespace("SELECT RealTimeProtectionEnabled, AntivirusSignatureAge FROM MSFT_MpComputerStatus", &mp, nsDefender); err == nil && len(mp) > 0 {
		rt := mp[0].RealTimeProtectionEnabled
		edad := mp[0].AntivirusSignatureAge
		s.ProteccionTiempoReal = &rt
		s.AntiguedadFirmasDias = &edad
	}
	if s.AntivirusNombre == "" && s.ProteccionTiempoReal == nil {
		return fmt.Errorf("no se pudo leer el estado del antivirus")
	}
	return nil
}

// ── utilidades ──────────────────────────────────────────────────────────

func limpiar(s string) string { return strings.TrimSpace(strings.Trim(s, "\x00")) }

func redondear(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func fechaOpcional(t time.Time) *time.Time {
	if t.IsZero() || t.Year() < 1990 {
		return nil
	}
	u := t.UTC()
	return &u
}

func noNulo(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
