package indexacion

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

type almacenFalso struct {
	mu          sync.Mutex
	pendientes  []ports.SKUPendiente
	quitados    []ports.SKUPendiente
	ocupado     bool
	ocupaciones int
	errLectura  error
	// alLeerDocs simula un cambio que llega mientras se arma el documento.
	alLeerDocs func()
}

func (a *almacenFalso) SKUsPendientes(_ context.Context, limite int) ([]ports.SKUPendiente, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.errLectura != nil {
		return nil, a.errLectura
	}
	return append([]ports.SKUPendiente(nil), a.pendientes[:min(limite, len(a.pendientes))]...), nil
}

func (a *almacenFalso) QuitarPendientes(_ context.Context, ps []ports.SKUPendiente) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.quitados = append(a.quitados, ps...)
	quedan := a.pendientes[:0]
	for _, p := range a.pendientes {
		borrar := false
		for _, q := range ps {
			borrar = borrar || q == p
		}
		if !borrar {
			quedan = append(quedan, p)
		}
	}
	a.pendientes = quedan
	return nil
}

func (a *almacenFalso) DocumentosDeProductos(_ context.Context, skus []string) ([]ports.DocumentoProducto, error) {
	if a.alLeerDocs != nil {
		a.alLeerDocs()
	}
	docs := make([]ports.DocumentoProducto, len(skus))
	for i, s := range skus {
		docs[i] = ports.DocumentoProducto{SKU: s}
	}
	return docs, nil
}

func (a *almacenFalso) TodosLosDocumentos(context.Context) ([]ports.DocumentoProducto, error) {
	return []ports.DocumentoProducto{{SKU: "TODO-1"}, {SKU: "TODO-2"}}, nil
}

func (a *almacenFalso) ConExclusionIndexacion(ctx context.Context, fn func(context.Context) error) (bool, error) {
	a.mu.Lock()
	ocupado := a.ocupado
	if a.ocupaciones > 0 {
		a.ocupaciones--
		ocupado = true
	}
	a.mu.Unlock()
	if ocupado {
		return false, nil
	}
	return true, fn(ctx)
}

type indiceFalso struct {
	mu             sync.Mutex
	cargado        bool
	errCargado     error
	errIndexar     error
	errReconstruir error
	indexados      []string
	reconstruidos  int
}

func (i *indiceFalso) IndiceCargado(context.Context) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.cargado, i.errCargado
}

func (i *indiceFalso) Indexar(_ context.Context, docs []ports.DocumentoProducto) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.errIndexar != nil {
		return i.errIndexar
	}
	for _, d := range docs {
		i.indexados = append(i.indexados, d.SKU)
	}
	return nil
}

func (i *indiceFalso) Reconstruir(context.Context, []ports.DocumentoProducto) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.errReconstruir != nil {
		return i.errReconstruir
	}
	i.reconstruidos++
	i.cargado = true
	return nil
}

func armar(a *almacenFalso, i *indiceFalso) *Indexador {
	return Nuevo(a, i, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
}

func TestVueltaIndexaYQuitaLoPendiente(t *testing.T) {
	a := &almacenFalso{pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}, {SKU: "B", Version: 3}}}
	i := &indiceFalso{cargado: true}
	if err := armar(a, i).Vuelta(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(i.indexados) != 2 || len(a.pendientes) != 0 {
		t.Errorf("indexados = %v, pendientes = %v", i.indexados, a.pendientes)
	}
}

func TestMotorCaidoNoPierdeLosPendientes(t *testing.T) {
	a := &almacenFalso{pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}}}
	i := &indiceFalso{cargado: true, errIndexar: errors.New("meilisearch caído")}
	x := armar(a, i)
	if err := x.Vuelta(context.Background()); err == nil {
		t.Fatal("tenía que informar el error")
	}
	if len(a.pendientes) != 1 {
		t.Fatalf("pendientes = %v: el SKU se perdió", a.pendientes)
	}
	// Vuelve el motor: el SKU se indexa sin que llegue otro evento.
	i.errIndexar = nil
	if err := x.Vuelta(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(i.indexados) != 1 || len(a.pendientes) != 0 {
		t.Errorf("indexados = %v, pendientes = %v", i.indexados, a.pendientes)
	}
}

func TestMotorCaidoAlMirarElIndiceNoIndexa(t *testing.T) {
	a := &almacenFalso{pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}}}
	i := &indiceFalso{errCargado: errors.New("meilisearch caído")}
	if err := armar(a, i).Vuelta(context.Background()); err == nil {
		t.Fatal("tenía que informar el error")
	}
	if i.reconstruidos != 0 || len(i.indexados) != 0 {
		t.Error("sin saber si el índice existe no se reconstruye: cargarlo lo crearía sin configuración")
	}
}

func TestIndiceInexistenteSeReconstruyeUnaVez(t *testing.T) {
	a := &almacenFalso{pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}}}
	i := &indiceFalso{}
	x := armar(a, i)
	for range 3 {
		if err := x.Vuelta(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if i.reconstruidos != 1 {
		t.Errorf("reconstrucciones = %d", i.reconstruidos)
	}
	if len(a.pendientes) != 0 {
		t.Errorf("pendientes = %v", a.pendientes)
	}
}

func TestOtraReplicaConLaExclusionNoEsUnError(t *testing.T) {
	a := &almacenFalso{ocupado: true, pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}}}
	i := &indiceFalso{}
	if err := armar(a, i).Vuelta(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i.reconstruidos != 0 || len(i.indexados) != 0 || len(a.pendientes) != 1 {
		t.Errorf("reconstruidos = %d, indexados = %v", i.reconstruidos, i.indexados)
	}
}

func TestLosPendientesSeLeenAntesQueLosDocumentos(t *testing.T) {
	// Un cambio que llega mientras se arma el documento sube la versión: lo que se
	// quita es la versión que se leyó, y la nueva sigue pendiente.
	a := &almacenFalso{pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}}}
	a.alLeerDocs = func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.pendientes[0].Version = 2
		a.alLeerDocs = nil
	}
	i := &indiceFalso{cargado: true}
	if err := armar(a, i).Vuelta(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.pendientes) != 1 || a.pendientes[0].Version != 2 {
		t.Errorf("pendientes = %v: el cambio nuevo se perdió", a.pendientes)
	}
}

func TestReconstruirEsperaSuTurno(t *testing.T) {
	a := &almacenFalso{ocupaciones: 2, pendientes: []ports.SKUPendiente{{SKU: "A", Version: 1}}}
	i := &indiceFalso{cargado: true}
	if err := armar(a, i).Reconstruir(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i.reconstruidos != 1 || len(a.pendientes) != 0 {
		t.Errorf("reconstruidos = %d, pendientes = %v", i.reconstruidos, a.pendientes)
	}
}

func TestReconstruirInformaLaFalla(t *testing.T) {
	a := &almacenFalso{}
	i := &indiceFalso{errReconstruir: errors.New("motor roto")}
	if err := armar(a, i).Reconstruir(context.Background()); err == nil {
		t.Error("tenía que fallar")
	}
}

func TestCorrerAtiendeElAvisoYTerminaAlCancelar(t *testing.T) {
	a := &almacenFalso{}
	i := &indiceFalso{cargado: true}
	x := Nuevo(a, i, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	ctx, cancelar := context.WithCancel(context.Background())
	listo := make(chan struct{})
	go func() { x.Correr(ctx); close(listo) }()

	a.mu.Lock()
	a.pendientes = []ports.SKUPendiente{{SKU: "A", Version: 1}}
	a.mu.Unlock()
	x.AvisarPendientes()
	x.AvisarPendientes() // un aviso de más no bloquea

	hasta := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		vacios := len(a.pendientes) == 0
		a.mu.Unlock()
		if vacios {
			break
		}
		if time.Now().After(hasta) {
			t.Fatal("el aviso no despertó al ciclo")
		}
		time.Sleep(time.Millisecond)
	}
	cancelar()
	select {
	case <-listo:
	case <-time.After(5 * time.Second):
		t.Fatal("Correr no volvió después de cancelar")
	}
}
