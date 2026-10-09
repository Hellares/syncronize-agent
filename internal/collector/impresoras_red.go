package collector

// Datos que Windows no tiene (tinta, contador de páginas, serie) y que se le
// preguntan a la IMPRESORA DE RED misma con los dos estándares que hablan casi
// todas (HP, Epson, Canon, Brother):
//
//   - IPP (RFC 8011), Get-Printer-Attributes: tintas (marker-*), estado y
//     alertas (printer-state-reasons), modelo y firmware.
//   - SNMP v2c, comunidad "public", Printer MIB (RFC 3805): contador total de
//     páginas y número de serie.
//
// Solo LECTURA: no se imprime ni se cambia nada. Las impresoras por USB o
// Bluetooth no tienen un estándar para esto (cada marca usa el suyo).

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	esperaIPP  = 4 * time.Second
	esperaSNMP = 2 * time.Second
	// Tope total para todas las impresoras: el reporte no puede quedar colgado
	// por una impresora que no contesta.
	esperaTotalImpresoras = 12 * time.Second
)

// completarImpresorasRed le pregunta a cada impresora con IP (en paralelo) y
// le suma lo que conteste. `ips` va de índice en la lista a host.
func completarImpresorasRed(imps []Impresora, ips map[int]string) {
	if len(ips) == 0 {
		return
	}
	type resultado struct {
		i int
		p Impresora
	}
	res := make(chan resultado, len(ips))
	var wg sync.WaitGroup
	for i, host := range ips {
		wg.Add(1)
		go func(i int, host string, p Impresora) {
			defer wg.Done()
			// Un pánico acá mataría el agente entero (no lo ve el recover de `paso`).
			defer func() { _ = recover() }()
			consultarImpresoraRed(host, &p)
			res <- resultado{i, p}
		}(i, host, imps[i])
	}
	listo := make(chan struct{})
	go func() { wg.Wait(); close(listo) }()
	select {
	case <-listo:
	case <-time.After(esperaTotalImpresoras):
	}
	for {
		select {
		case r := <-res:
			imps[r.i] = r.p
		default:
			unirDuplicadas(imps)
			return
		}
	}
}

// unirDuplicadas: una impresora por USB cuyo nombre o driver es EXACTAMENTE
// el modelo de UNA impresora de red con datos es la misma (Windows instala la
// L4260 dos veces: por USB y por WiFi). Copia los datos y anota de dónde.
func unirDuplicadas(imps []Impresora) {
	for i := range imps {
		u := &imps[i]
		if u.IP != "" || u.EnRed {
			continue
		}
		var fuente *Impresora
		n := 0
		for j := range imps {
			r := &imps[j]
			if r.IP == "" || r.Modelo == "" {
				continue
			}
			if strings.EqualFold(r.Modelo, u.Nombre) || strings.EqualFold(r.Modelo, u.Driver) {
				fuente = r
				n++
			}
		}
		if n != 1 {
			continue // ninguna o varias iguales: no se adivina
		}
		u.Fabricante, u.Modelo, u.Serie, u.Firmware = fuente.Fabricante, fuente.Modelo, fuente.Serie, fuente.Firmware
		u.ContadorPaginas, u.Tintas, u.Alertas = fuente.ContadorPaginas, fuente.Tintas, fuente.Alertas
		u.DatosDe = fuente.Nombre
	}
}

func consultarImpresoraRed(host string, p *Impresora) {
	p.IP = host
	var wg sync.WaitGroup
	var attrs map[string][]string
	var snmp map[string]string
	wg.Add(2)
	go func() { defer wg.Done(); attrs, _ = ippConsultar(host) }()
	go func() { defer wg.Done(); snmp, _ = snmpConsultar(host) }()
	wg.Wait()
	if attrs != nil {
		aplicarIPP(attrs, p)
	}
	aplicarSNMP(snmp, p)
}

// ── IPP ─────────────────────────────────────────────────────────────────

// Rutas habituales. Muchas exigen TLS (contestan 426 por http).
var rutasIPP = []string{"https://%s:631/ipp/print", "http://%s:631/ipp/print", "http://%s:631/ipp"}

var clienteIPP = &http.Client{
	Timeout: esperaIPP,
	Transport: &http.Transport{
		// Las impresoras traen un certificado autofirmado; solo se LEEN
		// atributos técnicos, no viaja nada sensible.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	},
}

func ippConsultar(host string) (map[string][]string, error) {
	var ultimo error
	for _, r := range rutasIPP {
		url := fmt.Sprintf(r, host)
		res, err := clienteIPP.Post(url, "application/ipp", bytes.NewReader(pedidoIPP(url)))
		if err != nil {
			ultimo = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 256<<10))
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			ultimo = fmt.Errorf("IPP %s: HTTP %d", url, res.StatusCode)
			continue
		}
		attrs, err := parsearIPP(b)
		if err != nil {
			ultimo = err
			continue
		}
		return attrs, nil
	}
	return nil, ultimo
}

func pedidoIPP(url string) []byte {
	uri := strings.Replace(strings.Replace(url, "https://", "ipps://", 1), "http://", "ipp://", 1)
	var b bytes.Buffer
	b.Write([]byte{2, 0, 0x00, 0x0B, 0, 0, 0, 1}) // IPP 2.0, Get-Printer-Attributes, request-id 1
	b.WriteByte(0x01)                             // operation-attributes
	atributo := func(tag byte, nombre, valor string) {
		b.WriteByte(tag)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(nombre)))
		b.WriteString(nombre)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(valor)))
		b.WriteString(valor)
	}
	atributo(0x47, "attributes-charset", "utf-8")
	atributo(0x48, "attributes-natural-language", "es")
	atributo(0x45, "printer-uri", uri)
	atributo(0x44, "requested-attributes", "all")
	b.WriteByte(0x03) // fin
	return b.Bytes()
}

// parsearIPP devuelve atributo → valores (los enteros como texto decimal).
func parsearIPP(b []byte) (map[string][]string, error) {
	if len(b) < 8 {
		return nil, errors.New("IPP: respuesta corta")
	}
	if estado := binary.BigEndian.Uint16(b[2:4]); estado >= 0x0100 {
		return nil, fmt.Errorf("IPP: estado 0x%04x", estado)
	}
	out := map[string][]string{}
	i, ultimo := 8, ""
	for i < len(b) {
		tag := b[i]
		i++
		if tag == 0x03 {
			break
		}
		if tag < 0x10 { // delimitador de grupo
			continue
		}
		if i+2 > len(b) {
			break
		}
		ln := int(binary.BigEndian.Uint16(b[i:]))
		i += 2
		if i+ln+2 > len(b) {
			break
		}
		nombre := string(b[i : i+ln])
		i += ln
		lv := int(binary.BigEndian.Uint16(b[i:]))
		i += 2
		if i+lv > len(b) {
			break
		}
		valor := b[i : i+lv]
		i += lv
		if nombre == "" {
			nombre = ultimo // valor adicional del mismo atributo
		}
		ultimo = nombre
		var s string
		switch {
		case (tag == 0x21 || tag == 0x23) && lv == 4: // integer, enum
			s = strconv.Itoa(int(int32(binary.BigEndian.Uint32(valor))))
		case tag == 0x22 && lv == 1: // boolean
			s = strconv.FormatBool(valor[0] == 1)
		case tag >= 0x40: // textos
			s = string(valor)
		default:
			continue
		}
		out[nombre] = append(out[nombre], s)
	}
	return out, nil
}

// Motivos de IPP que equivalen a los errores que ya entiende el backend.
var errorPorMotivo = map[string]string{
	"media-empty":         "SIN_PAPEL",
	"media-needed":        "SIN_PAPEL",
	"media-low":           "POCO_PAPEL",
	"media-jam":           "ATASCO",
	"door-open":           "PUERTA_ABIERTA",
	"cover-open":          "PUERTA_ABIERTA",
	"interlock-open":      "PUERTA_ABIERTA",
	"marker-supply-empty": "SIN_TONER",
	"toner-empty":         "SIN_TONER",
	"marker-supply-low":   "POCO_TONER",
	"toner-low":           "POCO_TONER",
	// Almohadillas / caja de mantenimiento llenas: va al taller.
	"marker-waste-full": "REQUIERE_SERVICIO",
}

// Del menos al más grave: si hay varios motivos, gana el peor.
var gravedadError = map[string]int{"POCO_PAPEL": 1, "POCO_TONER": 1, "PUERTA_ABIERTA": 2, "SIN_PAPEL": 3, "SIN_TONER": 3, "ATASCO": 3, "REQUIERE_SERVICIO": 3}

func aplicarIPP(a map[string][]string, p *Impresora) {
	primero := func(k string) string {
		if v := a[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	p.Modelo = primero("printer-make-and-model")
	p.Firmware = primero("printer-firmware-string-version")
	id := primero("printer-device-id")
	p.Fabricante = campoDeviceID(id, "MFG", "MANUFACTURER")
	if p.Modelo == "" {
		p.Modelo = strings.TrimSpace(p.Fabricante + " " + campoDeviceID(id, "MDL", "MODEL"))
	}

	nombres, colores, tipos := a["marker-names"], a["marker-colors"], a["marker-types"]
	niveles, bajos := a["marker-levels"], a["marker-low-levels"]
	for i, n := range nombres {
		t := Tinta{Nombre: n, Nivel: -2, Bajo: 15}
		if i < len(colores) {
			t.Color = colores[i]
		}
		if i < len(tipos) {
			t.Tipo = tipos[i]
		}
		if i < len(niveles) {
			if v, err := strconv.Atoi(niveles[i]); err == nil {
				t.Nivel = v
			}
		}
		if i < len(bajos) {
			if v, err := strconv.Atoi(bajos[i]); err == nil && v > 0 {
				t.Bajo = v
			}
		}
		p.Tintas = append(p.Tintas, t)
	}

	for _, m := range a["printer-state-reasons"] {
		if m == "none" || m == "" {
			continue
		}
		p.Alertas = append(p.Alertas, m)
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(m, "-error"), "-warning"), "-report")
		if e, ok := errorPorMotivo[base]; ok && gravedadError[e] > gravedadError[p.Error] {
			p.Error = e
		}
	}

	// Algunas impresoras dan el contador por IPP (las Epson no: va por SNMP).
	for _, k := range []string{"printer-impressions-completed", "printer-media-sheets-completed"} {
		if v, err := strconv.ParseInt(primero(k), 10, 64); err == nil && v > 0 && p.ContadorPaginas == nil {
			p.ContadorPaginas = &v
		}
	}
}

// campoDeviceID lee "MFG:EPSON;MDL:L4260 Series;..." (IEEE 1284).
func campoDeviceID(id string, claves ...string) string {
	for _, parte := range strings.Split(id, ";") {
		k, v, ok := strings.Cut(parte, ":")
		if !ok {
			continue
		}
		for _, c := range claves {
			if strings.EqualFold(strings.TrimSpace(k), c) {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// ── SNMP ────────────────────────────────────────────────────────────────

const (
	oidContador = "1.3.6.1.2.1.43.10.2.1.4.1.1" // prtMarkerLifeCount
	oidSerie    = "1.3.6.1.2.1.43.5.1.1.17.1"   // prtGeneralSerialNumber
	oidModelo   = "1.3.6.1.2.1.25.3.2.1.3.1"    // hrDeviceDescr
)

func aplicarSNMP(v map[string]string, p *Impresora) {
	if s := v[oidSerie]; s != "" {
		p.Serie = s
	}
	if c, err := strconv.ParseInt(v[oidContador], 10, 64); err == nil && c > 0 {
		p.ContadorPaginas = &c
	}
	if p.Modelo == "" {
		p.Modelo = v[oidModelo]
	}
}

// snmpConsultar hace UN GET v2c con los tres OIDs. Devuelve OID → valor
// (texto o entero en decimal); los que la impresora no tiene no aparecen.
func snmpConsultar(host string) (map[string]string, error) {
	c, err := net.DialTimeout("udp", net.JoinHostPort(host, "161"), esperaSNMP)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	pedido := pedidoSNMP("public", 1, []string{oidContador, oidSerie, oidModelo})
	buf := make([]byte, 4096)
	for intento := 0; intento < 2; intento++ { // UDP: un reintento
		_ = c.SetDeadline(time.Now().Add(esperaSNMP))
		if _, err = c.Write(pedido); err != nil {
			return nil, err
		}
		n, err2 := c.Read(buf)
		if err2 != nil {
			err = err2
			continue
		}
		return parsearSNMP(buf[:n])
	}
	return nil, err
}

func tlv(t byte, v []byte) []byte {
	switch {
	case len(v) < 0x80:
		return append([]byte{t, byte(len(v))}, v...)
	case len(v) <= 0xff:
		return append([]byte{t, 0x81, byte(len(v))}, v...)
	default:
		return append([]byte{t, 0x82, byte(len(v) >> 8), byte(len(v))}, v...)
	}
}

func codificarOID(s string) []byte {
	partes := strings.Split(s, ".")
	n := make([]int, len(partes))
	for i, p := range partes {
		n[i], _ = strconv.Atoi(p)
	}
	out := []byte{byte(40*n[0] + n[1])}
	for _, x := range n[2:] {
		enc := []byte{byte(x & 0x7f)}
		for x >>= 7; x > 0; x >>= 7 {
			enc = append([]byte{byte(x&0x7f | 0x80)}, enc...)
		}
		out = append(out, enc...)
	}
	return out
}

func decodificarOID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	partes := []string{strconv.Itoa(int(b[0]) / 40), strconv.Itoa(int(b[0]) % 40)}
	x := 0
	for _, by := range b[1:] {
		x = x<<7 | int(by&0x7f)
		if by&0x80 == 0 {
			partes = append(partes, strconv.Itoa(x))
			x = 0
		}
	}
	return strings.Join(partes, ".")
}

func pedidoSNMP(comunidad string, reqID int, oids []string) []byte {
	var vbs []byte
	for _, o := range oids {
		vbs = append(vbs, tlv(0x30, append(tlv(0x06, codificarOID(o)), 0x05, 0x00))...)
	}
	pdu := tlv(0x02, []byte{byte(reqID)})
	pdu = append(pdu, tlv(0x02, []byte{0})...)
	pdu = append(pdu, tlv(0x02, []byte{0})...)
	pdu = append(pdu, tlv(0x30, vbs)...)
	msg := tlv(0x02, []byte{1}) // versión 1 = SNMP v2c
	msg = append(msg, tlv(0x04, []byte(comunidad))...)
	msg = append(msg, tlv(0xa0, pdu)...) // GetRequest
	return tlv(0x30, msg)
}

var errSNMP = errors.New("SNMP: respuesta inválida")

// leerTLV lee un elemento BER y devuelve tag, contenido y el resto.
func leerTLV(b []byte) (byte, []byte, []byte, error) {
	if len(b) < 2 {
		return 0, nil, nil, errSNMP
	}
	t, l, i := b[0], int(b[1]), 2
	if l&0x80 != 0 {
		nb := l & 0x7f
		if nb == 0 || nb > 2 || len(b) < 2+nb {
			return 0, nil, nil, errSNMP
		}
		l = 0
		for k := 0; k < nb; k++ {
			l = l<<8 | int(b[2+k])
		}
		i += nb
	}
	if i+l > len(b) {
		return 0, nil, nil, errSNMP
	}
	return t, b[i : i+l], b[i+l:], nil
}

func parsearSNMP(b []byte) (map[string]string, error) {
	_, msg, _, err := leerTLV(b)
	if err != nil {
		return nil, err
	}
	resto := msg
	for k := 0; k < 2; k++ { // versión, comunidad
		if _, _, resto, err = leerTLV(resto); err != nil {
			return nil, err
		}
	}
	t, pdu, _, err := leerTLV(resto)
	if err != nil || t != 0xa2 { // GetResponse
		return nil, errSNMP
	}
	resto = pdu
	var estado []byte
	for k := 0; k < 3; k++ { // request-id, error-status, error-index
		var v []byte
		if _, v, resto, err = leerTLV(resto); err != nil {
			return nil, err
		}
		if k == 1 {
			estado = v
		}
	}
	if len(estado) > 0 && estado[len(estado)-1] != 0 {
		return nil, fmt.Errorf("SNMP: error-status %d", estado[len(estado)-1])
	}
	_, lista, _, err := leerTLV(resto)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for len(lista) > 0 {
		var vb []byte
		if _, vb, lista, err = leerTLV(lista); err != nil {
			return out, nil
		}
		_, oid, r, err := leerTLV(vb)
		if err != nil {
			continue
		}
		tv, val, _, err := leerTLV(r)
		if err != nil {
			continue
		}
		switch tv {
		case 0x02, 0x41, 0x42, 0x43, 0x46: // Integer, Counter32, Gauge32, TimeTicks, Counter64
			var x int64
			for _, by := range val {
				x = x<<8 | int64(by)
			}
			out[decodificarOID(oid)] = strconv.FormatInt(x, 10)
		case 0x04: // OCTET STRING
			s := strings.TrimSpace(strings.Trim(string(val), "\x00"))
			if s != "" {
				out[decodificarOID(oid)] = s
			}
		}
		// 0x80-0x82 = noSuchObject / noSuchInstance / endOfMibView: no está.
	}
	return out, nil
}
