package collector

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// respuestaIPP arma una respuesta Get-Printer-Attributes como la de la
// Epson L4260 (valores reales del 08-10).
func respuestaIPP() []byte {
	var b bytes.Buffer
	b.Write([]byte{2, 0, 0, 0, 0, 0, 0, 1}) // versión, status successful-ok, request-id
	b.WriteByte(0x04)                       // printer-attributes
	txt := func(tag byte, nombre, valor string) {
		b.WriteByte(tag)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(nombre)))
		b.WriteString(nombre)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(valor)))
		b.WriteString(valor)
	}
	ent := func(nombre string, v int32) {
		b.WriteByte(0x21)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(nombre)))
		b.WriteString(nombre)
		_ = binary.Write(&b, binary.BigEndian, uint16(4))
		_ = binary.Write(&b, binary.BigEndian, v)
	}
	txt(0x41, "printer-make-and-model", "EPSON L4260 Series")
	txt(0x41, "printer-firmware-string-version", "05.16.XJ05Q2")
	txt(0x41, "printer-device-id", "MFG:EPSON;CMD:ESCPL2;MDL:L4260 Series;SN:584145;")
	txt(0x41, "marker-names", "Black ink")
	txt(0x41, "", "Cyan ink") // valor adicional: nombre vacío
	txt(0x41, "marker-colors", "#000000")
	txt(0x41, "", "#00FFFF")
	txt(0x44, "marker-types", "ink")
	txt(0x44, "", "ink")
	ent("marker-levels", 68)
	ent("", 9)
	ent("marker-low-levels", 15)
	ent("", 15)
	txt(0x44, "printer-state-reasons", "marker-supply-low-warning")
	txt(0x44, "", "media-jam-error")
	b.WriteByte(0x03)
	return b.Bytes()
}

func TestIPPEpson(t *testing.T) {
	a, err := parsearIPP(respuestaIPP())
	if err != nil {
		t.Fatal(err)
	}
	var p Impresora
	aplicarIPP(a, &p)
	if p.Modelo != "EPSON L4260 Series" || p.Fabricante != "EPSON" || p.Firmware != "05.16.XJ05Q2" {
		t.Fatalf("identidad: %+v", p)
	}
	if len(p.Tintas) != 2 || p.Tintas[1] != (Tinta{Nombre: "Cyan ink", Color: "#00FFFF", Tipo: "ink", Nivel: 9, Bajo: 15}) {
		t.Fatalf("tintas: %+v", p.Tintas)
	}
	// Con varios motivos gana el más grave (atasco > poca tinta).
	if p.Error != "ATASCO" || len(p.Alertas) != 2 {
		t.Fatalf("error=%q alertas=%v", p.Error, p.Alertas)
	}
}

func TestIPPRechazaEstadoDeError(t *testing.T) {
	r := respuestaIPP()
	r[2], r[3] = 0x04, 0x00 // client-error-bad-request
	if _, err := parsearIPP(r); err == nil {
		t.Fatal("esperaba error")
	}
	if _, err := parsearIPP(r[:5]); err == nil {
		t.Fatal("esperaba error por respuesta corta")
	}
	// Truncada a la mitad: no entra en pánico.
	if _, err := parsearIPP(respuestaIPP()[:60]); err != nil {
		t.Fatal(err)
	}
}

func TestSNMPIdaYVuelta(t *testing.T) {
	if got := decodificarOID(codificarOID(oidContador)); got != oidContador {
		t.Fatalf("OID %s", got)
	}
	vb := func(oid string, valor []byte) []byte {
		return tlv(0x30, append(tlv(0x06, codificarOID(oid)), valor...))
	}
	var vbs []byte
	vbs = append(vbs, vb(oidContador, tlv(0x41, []byte{0x26, 0x57}))...) // Counter32 9815
	vbs = append(vbs, vb(oidSerie, tlv(0x04, []byte("XAEY068888")))...)
	vbs = append(vbs, vb(oidModelo, []byte{0x80, 0x00})...) // noSuchObject
	pdu := append(append(append(tlv(0x02, []byte{1}), tlv(0x02, []byte{0})...), tlv(0x02, []byte{0})...), tlv(0x30, vbs)...)
	msg := tlv(0x30, append(append(tlv(0x02, []byte{1}), tlv(0x04, []byte("public"))...), tlv(0xa2, pdu)...))

	v, err := parsearSNMP(msg)
	if err != nil {
		t.Fatal(err)
	}
	var p Impresora
	aplicarSNMP(v, &p)
	if p.Serie != "XAEY068888" || p.ContadorPaginas == nil || *p.ContadorPaginas != 9815 || p.Modelo != "" {
		t.Fatalf("%+v (contador %v)", p, p.ContadorPaginas)
	}
	if _, err := parsearSNMP(msg[:10]); err == nil {
		t.Fatal("esperaba error con la respuesta truncada")
	}
}

func TestPedidoSNMPBienFormado(t *testing.T) {
	_, msg, _, err := leerTLV(pedidoSNMP("public", 1, []string{oidContador, oidSerie}))
	if err != nil {
		t.Fatal(err)
	}
	_, ver, resto, _ := leerTLV(msg)
	_, com, resto, _ := leerTLV(resto)
	tag, _, _, _ := leerTLV(resto)
	if ver[0] != 1 || string(com) != "public" || tag != 0xa0 {
		t.Fatalf("versión %v comunidad %q pdu 0x%x", ver, com, tag)
	}
}

func TestUnirDuplicadas(t *testing.T) {
	c := int64(9815)
	imps := []Impresora{
		{Nombre: "EPSONAF5ABD (L4260 Series)", Puerto: "WSD-1", EnRed: true, IP: "192.168.100.4", Modelo: "EPSON L4260 Series", Serie: "XAEY068888", ContadorPaginas: &c},
		{Nombre: "EPSON L4260 Series", Driver: "EPSON L4260 Series", Puerto: "USB001"},
		{Nombre: "Termica BT", Driver: "Generic / Text Only", Puerto: "COM13:"},
	}
	unirDuplicadas(imps)
	if imps[1].DatosDe != imps[0].Nombre || imps[1].Serie != "XAEY068888" || *imps[1].ContadorPaginas != 9815 {
		t.Fatalf("USB: %+v", imps[1])
	}
	if imps[2].DatosDe != "" || imps[2].Serie != "" {
		t.Fatalf("la térmica no es la misma: %+v", imps[2])
	}
	// Dos de red del mismo modelo: no se adivina cuál es la de USB.
	imps = append(imps[:2:2], Impresora{Nombre: "Otra", IP: "10.0.0.2", Modelo: "EPSON L4260 Series"})
	imps[1] = Impresora{Nombre: "EPSON L4260 Series", Puerto: "USB001"}
	unirDuplicadas(imps)
	if imps[1].DatosDe != "" {
		t.Fatal("con dos candidatas no debe unir")
	}
}
