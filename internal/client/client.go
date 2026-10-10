// Package client habla con el backend de Syncronize.
//
// Contrato:
//
//	POST {servidor}/agentes/vincular   {codigo, reporte}       → {agenteId, token, intervaloSegundos, empresa}
//	POST {servidor}/agentes/reporte    Bearer token, Reporte   → {intervaloSegundos?, continuo?}
//	GET  {servidor}/agentes/pedido     Bearer token            → {reportar, continuo, intervaloSegundos, soporteRemoto?}
//	POST {servidor}/agentes/soporte-remoto  Bearer token, {permitido, estado?, id?, detalle?, version?}
//
// /pedido es un long-poll: el backend la retiene ~25 s y contesta antes si
// alguien pide datos desde el panel o cambia el modo.
//
// 401/403 = token revocado: el agente deja de reportar.
package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrRevocado = errors.New("el token del agente fue revocado")

// ErrRechazado: el backend no acepta ese reporte (400/413/422). Reintentarlo no
// sirve; se descarta para no trabar la cola.
var ErrRechazado = errors.New("el servidor rechazó el reporte")

// ErrSinPedidos: el servidor todavía no tiene /agentes/pedido (versión vieja).
// El agente vuelve a reportar cada intervalo, como antes.
var ErrSinPedidos = errors.New("el servidor no atiende pedidos")

type Client struct {
	Servidor string
	Version  string
	http     *http.Client
	// Para el long-poll: más largo que lo que el backend retiene la pregunta.
	largo *http.Client
}

func New(servidor, version string) *Client {
	return &Client{
		Servidor: strings.TrimRight(servidor, "/"),
		Version:  version,
		http:     &http.Client{Timeout: 30 * time.Second},
		largo:    &http.Client{Timeout: 60 * time.Second},
	}
}

type RespuestaVincular struct {
	AgenteID          string `json:"agenteId"`
	Token             string `json:"token"`
	IntervaloSegundos int    `json:"intervaloSegundos"`
	// Nombre del servicio técnico con el que quedó vinculado ("Taller A").
	Empresa string `json:"empresa"`
}

type RespuestaReporte struct {
	IntervaloSegundos int `json:"intervaloSegundos"`
	// nil = servidor viejo (no lo manda).
	Continuo *bool `json:"continuo"`
}

type RespuestaPedido struct {
	// Alguien pidió los datos actuales desde el panel.
	Reportar          bool `json:"reportar"`
	Continuo          bool `json:"continuo"`
	IntervaloSegundos int  `json:"intervaloSegundos"`
	// nil = servidor viejo (sin soporte remoto).
	SoporteRemoto *PedidoSoporte `json:"soporteRemoto"`
}

// PedidoSoporte: el técnico encendió el soporte remoto desde el panel.
type PedidoSoporte struct {
	// Segundos que le quedan al soporte encendido: nil = apagado (no lo
	// pidieron o lo terminaron); 0 = se venció el plazo. Van segundos y no una
	// hora porque el reloj del equipo puede estar mal.
	RestanteSegundos *int `json:"restanteSegundos"`
	// Servidor de RustDesk; vacío = el público.
	Host string `json:"host"`
	Key  string `json:"key"`
}

// EstadoSoporte es lo que el agente le informa al backend sobre RustDesk.
type EstadoSoporte struct {
	Permitido bool   `json:"permitido"`
	Estado    string `json:"estado,omitempty"`
	ID        string `json:"id,omitempty"`
	Detalle   string `json:"detalle,omitempty"`
	Version   string `json:"version,omitempty"`
}

// Vincular canjea el código de un solo uso que genera Syncronize por un token.
func (c *Client) Vincular(codigo string, reporte any) (*RespuestaVincular, error) {
	cuerpo, err := json.Marshal(map[string]any{"codigo": codigo, "reporte": reporte})
	if err != nil {
		return nil, err
	}
	var out RespuestaVincular
	estado, err := c.post("/agentes/vincular", "", cuerpo, &out)
	if err != nil {
		return nil, err
	}
	if estado == http.StatusNotFound || estado == http.StatusBadRequest || estado == http.StatusUnauthorized {
		return nil, errors.New("código inválido o vencido: genera uno nuevo en Syncronize")
	}
	if estado/100 != 2 {
		return nil, fmt.Errorf("el servidor respondió %d", estado)
	}
	if out.Token == "" {
		return nil, errors.New("el servidor no devolvió un token")
	}
	return &out, nil
}

// EnviarReporte manda un reporte ya serializado (puede venir de la cola).
func (c *Client) EnviarReporte(token string, reporteJSON []byte) (*RespuestaReporte, error) {
	var out RespuestaReporte
	estado, err := c.post("/agentes/reporte", token, reporteJSON, &out)
	if err != nil {
		return nil, err
	}
	switch {
	case estado == http.StatusUnauthorized || estado == http.StatusForbidden:
		return nil, ErrRevocado
	case estado == http.StatusBadRequest || estado == http.StatusRequestEntityTooLarge || estado == http.StatusUnprocessableEntity:
		return nil, fmt.Errorf("%w (%d)", ErrRechazado, estado)
	case estado/100 != 2:
		return nil, fmt.Errorf("el servidor respondió %d", estado)
	}
	return &out, nil
}

// InformarSoporte le dice al backend si el cliente autorizó el soporte remoto
// y cómo está RustDesk (con el ID para conectarse).
func (c *Client) InformarSoporte(token string, e EstadoSoporte) error {
	cuerpo, err := json.Marshal(e)
	if err != nil {
		return err
	}
	estado, err := c.post("/agentes/soporte-remoto", token, cuerpo, nil)
	if err != nil {
		return err
	}
	switch {
	case estado == http.StatusUnauthorized || estado == http.StatusForbidden:
		return ErrRevocado
	case estado == http.StatusNotFound:
		return ErrSinPedidos
	case estado/100 != 2:
		return fmt.Errorf("el servidor respondió %d", estado)
	}
	return nil
}

// EsperarPedido pregunta si hay un pedido de datos. Tarda hasta ~25 s si no
// lo hay (long-poll); ctx la corta al detener el servicio.
func (c *Client) EsperarPedido(ctx context.Context, token string) (*RespuestaPedido, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Servidor+"/agentes/pedido", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "syncronize-agent/"+c.Version)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.largo.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, ErrRevocado
	case res.StatusCode == http.StatusNotFound:
		return nil, ErrSinPedidos
	case res.StatusCode/100 != 2:
		return nil, fmt.Errorf("el servidor respondió %d", res.StatusCode)
	}
	var out RespuestaPedido
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("respuesta inválida del servidor: %w", err)
	}
	return &out, nil
}

func (c *Client) post(ruta, token string, cuerpo []byte, out any) (int, error) {
	// gzip: un reporte pesa ~5 KB y baja a ~1 KB (el backend ya acepta gzip).
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(cuerpo); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, c.Servidor+ruta, &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("User-Agent", "syncronize-agent/"+c.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 == 2 && len(b) > 0 && out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, fmt.Errorf("respuesta inválida del servidor: %w", err)
		}
	}
	return res.StatusCode, nil
}
