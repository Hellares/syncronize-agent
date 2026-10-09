package agent

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/Hellares/syncronize-agent/internal/client"
	"github.com/Hellares/syncronize-agent/internal/config"
)

// pedidosFalsos contesta lo que tiene en la lista y después espera a que el
// test termine (como un long-poll sin pedidos).
type pedidosFalsos struct {
	mu         sync.Mutex
	respuestas []any // *client.RespuestaPedido o error
	llamadas   int
}

func (p *pedidosFalsos) EsperarPedido(ctx context.Context, _ string) (*client.RespuestaPedido, error) {
	p.mu.Lock()
	p.llamadas++
	if len(p.respuestas) == 0 {
		p.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	r := p.respuestas[0]
	p.respuestas = p.respuestas[1:]
	p.mu.Unlock()
	if err, ok := r.(error); ok {
		return nil, err
	}
	return r.(*client.RespuestaPedido), nil
}

func agenteDePrueba(v *config.Vinculo, respuestas ...any) (*Agente, *pedidosFalsos, *int) {
	p := &pedidosFalsos{respuestas: respuestas}
	reportes := 0
	a := &Agente{v: v, ped: p, log: log.New(io.Discard, "", 0), cfgMu: &sync.Mutex{}}
	a.reportar = func() error { reportes++; return nil }
	a.guardar = func() error { return nil }
	return a, p, &reportes
}

// escuchar corre escucharPedidos hasta que se acaban las respuestas.
func escuchar(t *testing.T, a *Agente, p *pedidosFalsos, n int) error {
	t.Helper()
	ctx, cancelar := context.WithCancel(context.Background())
	fin := make(chan error, 1)
	go func() { fin <- a.escucharPedidos(ctx) }()
	limite := time.After(5 * time.Second)
	for {
		p.mu.Lock()
		listo := p.llamadas > n
		p.mu.Unlock()
		select {
		case err := <-fin:
			cancelar()
			return err
		case <-limite:
			cancelar()
			t.Fatal("escucharPedidos no avanzó")
		default:
		}
		if listo {
			cancelar()
			return <-fin
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAPedidoSoloReportaCuandoLoPiden(t *testing.T) {
	cfg := &config.Vinculo{Token: "t"}
	a, p, reportes := agenteDePrueba(cfg,
		&client.RespuestaPedido{Reportar: false},
		&client.RespuestaPedido{Reportar: false},
		&client.RespuestaPedido{Reportar: true},
	)
	if err := escuchar(t, a, p, 3); err != nil {
		t.Fatal(err)
	}
	if *reportes != 1 {
		t.Fatalf("reportes = %d, quería 1 (solo el pedido)", *reportes)
	}
	if a.continuo() {
		t.Fatal("a pedido no debe reportar cada intervalo")
	}
}

func TestPasarAContinuoReportaEnElActo(t *testing.T) {
	cfg := &config.Vinculo{Token: "t"}
	a, p, reportes := agenteDePrueba(cfg,
		&client.RespuestaPedido{Continuo: true, IntervaloSegundos: 600},
		&client.RespuestaPedido{Continuo: true, IntervaloSegundos: 600},
	)
	if err := escuchar(t, a, p, 2); err != nil {
		t.Fatal(err)
	}
	if *reportes != 1 || !cfg.Continuo || cfg.IntervaloSegundos != 600 {
		t.Fatalf("reportes=%d continuo=%v intervalo=%d", *reportes, cfg.Continuo, cfg.IntervaloSegundos)
	}
	if !a.continuo() {
		t.Fatal("debería reportar cada intervalo")
	}
}

func TestServidorViejoVuelveAlIntervalo(t *testing.T) {
	cfg := &config.Vinculo{Token: "t"}
	a, p, _ := agenteDePrueba(cfg, client.ErrSinPedidos)
	ctx, cancelar := context.WithCancel(context.Background())
	go func() { _ = a.escucharPedidos(ctx) }()
	defer cancelar()
	for i := 0; i < 500 && !a.continuo(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !a.continuo() {
		t.Fatal("sin /agentes/pedido debe reportar cada intervalo, como antes")
	}
	_ = p
}

func TestRevocadoCortaLaEscucha(t *testing.T) {
	a, p, _ := agenteDePrueba(&config.Vinculo{Token: "t"}, client.ErrRevocado)
	if err := escuchar(t, a, p, 5); !errors.Is(err, client.ErrRevocado) {
		t.Fatalf("err = %v, quería ErrRevocado", err)
	}
}
