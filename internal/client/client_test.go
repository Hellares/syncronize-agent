package client

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func servidor(t *testing.T, h func(w http.ResponseWriter, ruta, auth string, cuerpo map[string]any)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("falta Content-Encoding gzip")
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(zr)
		var cuerpo map[string]any
		if err := json.Unmarshal(b, &cuerpo); err != nil {
			t.Fatalf("cuerpo no es JSON: %v", err)
		}
		h(w, r.URL.Path, r.Header.Get("Authorization"), cuerpo)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestVincular(t *testing.T) {
	s := servidor(t, func(w http.ResponseWriter, ruta, auth string, c map[string]any) {
		if ruta != "/api/agentes/vincular" || auth != "" || c["codigo"] != "ABC123" || c["reporte"] == nil {
			t.Errorf("petición inesperada: %s %q %v", ruta, auth, c)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"agenteId":"ag1","token":"tok","intervaloSegundos":900}`))
	})
	res, err := New(s.URL+"/api/", "test").Vincular("ABC123", map[string]string{"x": "y"})
	if err != nil || res.Token != "tok" || res.AgenteID != "ag1" || res.IntervaloSegundos != 900 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestVincularCodigoInvalido(t *testing.T) {
	s := servidor(t, func(w http.ResponseWriter, _, _ string, _ map[string]any) { w.WriteHeader(404) })
	if _, err := New(s.URL, "test").Vincular("NOPE", nil); err == nil {
		t.Fatal("esperaba error")
	}
}

func TestEnviarReporte(t *testing.T) {
	casos := []struct {
		estado int
		quiere error
	}{
		{200, nil},
		{401, ErrRevocado},
		{403, ErrRevocado},
		{413, ErrRechazado},
		{400, ErrRechazado},
	}
	for _, c := range casos {
		s := servidor(t, func(w http.ResponseWriter, ruta, auth string, _ map[string]any) {
			if ruta != "/agentes/reporte" || auth != "Bearer tok" {
				t.Errorf("petición inesperada: %s %q", ruta, auth)
			}
			w.WriteHeader(c.estado)
			if c.estado == 200 {
				_, _ = w.Write([]byte(`{"intervaloSegundos":300}`))
			}
		})
		res, err := New(s.URL, "test").EnviarReporte("tok", []byte(`{"version":1}`))
		if !errors.Is(err, c.quiere) {
			t.Errorf("%d: err=%v, quería %v", c.estado, err, c.quiere)
		}
		if c.estado == 200 && res.IntervaloSegundos != 300 {
			t.Errorf("intervalo=%d", res.IntervaloSegundos)
		}
	}
	// 500: error común (reintentar), ni revocado ni rechazado.
	s := servidor(t, func(w http.ResponseWriter, _, _ string, _ map[string]any) { w.WriteHeader(502) })
	_, err := New(s.URL, "test").EnviarReporte("tok", []byte(`{}`))
	if err == nil || errors.Is(err, ErrRevocado) || errors.Is(err, ErrRechazado) {
		t.Errorf("502: err=%v", err)
	}
}

func TestEsperarPedido(t *testing.T) {
	casos := []struct {
		estado int
		cuerpo string
		err    error
	}{
		{200, `{"reportar":true,"continuo":false,"intervaloSegundos":900}`, nil},
		{401, ``, ErrRevocado},
		{404, ``, ErrSinPedidos},
	}
	for _, c := range casos {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/agentes/pedido" || r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("petición inesperada: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			}
			w.WriteHeader(c.estado)
			_, _ = w.Write([]byte(c.cuerpo))
		}))
		res, err := New(s.URL, "test").EsperarPedido(context.Background(), "tok")
		s.Close()
		if !errors.Is(err, c.err) {
			t.Fatalf("%d: err = %v, quería %v", c.estado, err, c.err)
		}
		if c.err == nil && (!res.Reportar || res.Continuo || res.IntervaloSegundos != 900) {
			t.Fatalf("res = %+v", res)
		}
	}
}
