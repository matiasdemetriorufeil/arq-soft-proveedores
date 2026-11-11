//go:build integration

package mongo_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	driver "go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/adapters/mongo"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/sincronizacion"
)

// La misma imagen que el docker-compose.yml. En 8.0 el TCMalloc que trae
// MongoDB rompe el ABI de rseq y el servidor se niega a arrancar sobre un
// kernel 6.19 o más nuevo, que es el que traen los runners de GitHub
// (SERVER-121912): los tests de integración fallaban solo en CI.
const imagenMongo = "mongo:9.0"

var cliente *driver.Client

func TestMain(m *testing.M) {
	os.Exit(correr(m))
}

func correr(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcmongo.Run(ctx, imagenMongo)
	if ctr != nil {
		defer func() {
			if err := testcontainers.TerminateContainer(ctr); err != nil {
				fmt.Fprintf(os.Stderr, "no se pudo terminar el contenedor de MongoDB: %v\n", err)
			}
		}()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tests de integración: no se pudo levantar %s con testcontainers "+
			"(¿Docker está instalado y corriendo?): %v\n", imagenMongo, err)
		return 1
	}
	uri, err := ctr.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cliente, err = mongo.Conectar(uri)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cliente.Disconnect(ctx)
	return m.Run()
}

// almacenNuevo usa una base por test, así no se pisan.
func almacenNuevo(t *testing.T) *mongo.Almacen {
	t.Helper()
	a := mongo.Nuevo(cliente, "test_"+mensajeria.NuevoID()[:8], "fav")
	if err := a.CrearIndices(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

func importacionConEvento(t *testing.T, sec int64, terminada time.Time) sincronizacion.Importacion {
	t.Helper()
	sobre, err := mensajeria.NuevoSobre(context.Background(), mensajeria.TipoListaPreciosActualizada, 1, terminada,
		map[string]int64{"secuencia": sec})
	if err != nil {
		t.Fatal(err)
	}
	return sincronizacion.Importacion{
		ID: mensajeria.NuevoID(), Proveedor: "fav", IniciadaEn: terminada, TerminadaEn: terminada,
		Estado: sincronizacion.EstadoConCambios, Secuencia: sec,
		Crudo:    []byte(`{"items":[{"productoId":"p","campoNuevoDeFAV":{"x":1}}]}`),
		Vigentes: []dominio.Item{{SKU: "FAV-v1", Nombre: "Remera", Categoria: "ropa-remeras", Unidad: "unidad", PrecioUnitario: 15000, StockDisponible: 10}},
		Evento:   &sincronizacion.EventoOutbox{Sobre: sobre},
	}
}

func TestGuardaYDevuelveLaUltimaVigenteConSuEvento(t *testing.T) {
	ctx := context.Background()
	a := almacenNuevo(t)
	t0 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	if _, ok, err := a.UltimaVigente(ctx); err != nil || ok {
		t.Fatalf("base vacía: ok = %v, err = %v", ok, err)
	}
	uno := importacionConEvento(t, 1, t0)
	dos := importacionConEvento(t, 2, t0.Add(5*time.Minute))
	for _, imp := range []sincronizacion.Importacion{uno, dos} {
		if err := a.Guardar(ctx, imp); err != nil {
			t.Fatal(err)
		}
	}
	// Un error no cambia la vigente ni la última sincronización buena.
	if err := a.Guardar(ctx, sincronizacion.Importacion{
		ID: mensajeria.NuevoID(), Proveedor: "fav", Estado: sincronizacion.EstadoError,
		IniciadaEn: t0.Add(time.Hour), TerminadaEn: t0.Add(time.Hour), Error: "FAV no está disponible",
	}); err != nil {
		t.Fatal(err)
	}

	v, ok, err := a.UltimaVigente(ctx)
	if err != nil || !ok || v.ID != dos.ID || len(v.Vigentes) != 1 || v.Vigentes[0] != dos.Vigentes[0] {
		t.Fatalf("vigente = %+v, ok = %v, err = %v", v, ok, err)
	}
	if v.Evento == nil || v.Evento.Sobre.EventoID != dos.Evento.Sobre.EventoID || string(v.Evento.Sobre.Datos) != string(dos.Evento.Sobre.Datos) {
		t.Errorf("el sobre no volvió igual: %+v", v.Evento)
	}
	ult, ok, err := a.UltimaSincronizacionOK(ctx)
	if err != nil || !ok || !ult.Equal(dos.TerminadaEn) {
		t.Errorf("última OK = %v, ok = %v, err = %v", ult, ok, err)
	}
}

func TestOutboxPendientesEnOrdenYMarcarPublicado(t *testing.T) {
	ctx := context.Background()
	a := almacenNuevo(t)
	t0 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	// Se guardan desordenados: el outbox tiene que devolverlos por secuencia.
	tres, uno, dos := importacionConEvento(t, 3, t0), importacionConEvento(t, 1, t0), importacionConEvento(t, 2, t0)
	for _, imp := range []sincronizacion.Importacion{tres, uno, dos} {
		if err := a.Guardar(ctx, imp); err != nil {
			t.Fatal(err)
		}
	}
	p, err := a.Pendientes(ctx, 10)
	if err != nil || len(p) != 3 || p[0].Secuencia != 1 || p[2].Secuencia != 3 {
		t.Fatalf("pendientes = %+v, err = %v", p, err)
	}
	if err := a.MarcarPublicado(ctx, uno.ID, t0); err != nil {
		t.Fatal(err)
	}
	if n, _ := a.ContarPendientes(ctx); n != 2 {
		t.Errorf("pendientes = %d, quiero 2", n)
	}
	if err := a.MarcarPublicado(ctx, "no-existe", t0); err == nil {
		t.Error("marcar una importación inexistente tenía que fallar")
	}
}

func TestDosImportacionesConLaMismaSecuenciaChocan(t *testing.T) {
	ctx := context.Background()
	a := almacenNuevo(t)
	t0 := time.Now().UTC()
	if err := a.Guardar(ctx, importacionConEvento(t, 1, t0)); err != nil {
		t.Fatal(err)
	}
	if err := a.Guardar(ctx, importacionConEvento(t, 1, t0)); err == nil {
		t.Error("el índice único de secuencia no frenó un duplicado")
	}
}

func TestPingConLaBaseArriba(t *testing.T) {
	if err := almacenNuevo(t).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
