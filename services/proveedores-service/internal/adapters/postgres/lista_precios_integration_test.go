//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

// proveedorFAV existe por 003_proveedor_fav.sql.
const proveedorFAV = int64(11)

// cambio toma la secuencia del reloj: cada cambio de estos tests es más nuevo que el
// anterior, aunque compartan la base.
func cambio(completa bool, productos []string, ofertas map[string]float64, bajas []string) ports.CambioListaPrecios {
	c := ports.CambioListaPrecios{
		EventoID:    fmt.Sprintf("evento-%d", time.Now().UnixNano()),
		ProveedorID: proveedorFAV,
		Secuencia:   time.Now().UnixNano(),
		Completa:    completa,
		Bajas:       bajas,
	}
	for _, sku := range productos {
		c.Productos = append(c.Productos, ports.ProductoCambio{SKU: sku, Nombre: "Producto " + sku, Categoria: "ropa-prueba", Unidad: "unidad"})
	}
	for sku, precio := range ofertas {
		c.Ofertas = append(c.Ofertas, ports.OfertaCambio{ProductoSKU: sku, PrecioUnitario: precio, DescuentoPct: 10, CantidadMinimaDescuento: 2, Flete: 1.5, StockDisponible: 10})
	}
	return c
}

func aplicar(t *testing.T, repo *postgres.Repo, c ports.CambioListaPrecios) ports.ResultadoAplicacion {
	t.Helper()
	res, err := repo.AplicarListaPrecios(context.Background(), c)
	if err != nil {
		t.Fatalf("AplicarListaPrecios: %v", err)
	}
	return res
}

func ofertaFAV(t *testing.T, repo *postgres.Repo, sku string) (domain.Oferta, bool) {
	t.Helper()
	ofertas, err := repo.OfertasPorProducto(context.Background(), sku)
	if err != nil {
		t.Fatalf("OfertasPorProducto(%s): %v", sku, err)
	}
	for _, o := range ofertas {
		if o.ProveedorID == proveedorFAV {
			return o, true
		}
	}
	return domain.Oferta{}, false
}

// limpiarFAV deja la base como la dejó el seed, antes y después de cada test: los
// otros tests del paquete la comparten y cuentan sus filas.
func limpiarFAV(t *testing.T) {
	t.Helper()
	borrar := func() {
		for _, sql := range []string{
			`DELETE FROM ofertas WHERE proveedor_id = 11`,
			`DELETE FROM productos WHERE sku LIKE 'FAV-%'`,
			`DELETE FROM skus_por_reindexar`,
		} {
			if _, err := poolCompartido.Exec(context.Background(), sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	borrar()
	t.Cleanup(borrar)
}

func TestEventoNuevoApareceEnElRanking(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	res := aplicar(t, repo, cambio(true, []string{"FAV-a"}, map[string]float64{"FAV-a": 38000.5}, nil))
	if !res.Aplicado || !slices.Equal(res.SKUs, []string{"FAV-a"}) {
		t.Fatalf("resultado = %+v", res)
	}
	o, ok := ofertaFAV(t, repo, "FAV-a")
	if !ok || o.PrecioUnitario != 38000.5 || o.DescuentoPct != 10 || o.CantidadMinimaDescuento != 2 || o.Flete != 1.5 || o.StockDisponible != 10 {
		t.Errorf("oferta = %+v", o)
	}
	if o.ProveedorNombre != "FAV (tienda de ropa)" {
		t.Errorf("proveedor = %q", o.ProveedorNombre)
	}
}

func TestDuplicadoSeAplicaUnaVez(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	c := cambio(false, []string{"FAV-dup"}, map[string]float64{"FAV-dup": 100}, nil)
	aplicar(t, repo, c)

	// El mismo id con otro contenido y una secuencia mayor: si se aplicara,
	// cambiaría el precio.
	otra := c
	otra.Secuencia += 1000
	otra.Ofertas = []ports.OfertaCambio{{ProductoSKU: "FAV-dup", PrecioUnitario: 1, StockDisponible: 1}}
	res := aplicar(t, repo, otra)
	if res.Aplicado || res.Motivo != postgres.MotivoDuplicado || len(res.SKUs) != 0 {
		t.Errorf("resultado = %+v", res)
	}
	if o, _ := ofertaFAV(t, repo, "FAV-dup"); o.PrecioUnitario != 100 {
		t.Errorf("precio = %v", o.PrecioUnitario)
	}
}

func TestSecuenciaMenorOIgualSinCambios(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	c := cambio(false, []string{"FAV-sec"}, map[string]float64{"FAV-sec": 100}, nil)
	aplicar(t, repo, c)

	for nombre, secuencia := range map[string]int64{"igual": c.Secuencia, "menor": c.Secuencia - 1} {
		t.Run(nombre, func(t *testing.T) {
			vieja := cambio(false, nil, map[string]float64{"FAV-sec": 1}, nil)
			vieja.Secuencia = secuencia
			res := aplicar(t, repo, vieja)
			if res.Aplicado || res.Motivo != postgres.MotivoSecuenciaVieja {
				t.Errorf("resultado = %+v", res)
			}
			if o, _ := ofertaFAV(t, repo, "FAV-sec"); o.PrecioUnitario != 100 {
				t.Errorf("precio = %v", o.PrecioUnitario)
			}
		})
	}
}

func TestBajaBorraLaOfertaYElProductoQueda(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	aplicar(t, repo, cambio(false, []string{"FAV-b1", "FAV-b2"}, map[string]float64{"FAV-b1": 100, "FAV-b2": 200}, nil))
	res := aplicar(t, repo, cambio(false, nil, nil, []string{"FAV-b1", "FAV-nunca-existio"}))
	if !res.Aplicado || !slices.Equal(res.SKUs, []string{"FAV-b1", "FAV-nunca-existio"}) {
		t.Fatalf("resultado = %+v", res)
	}
	if _, ok := ofertaFAV(t, repo, "FAV-b1"); ok {
		t.Error("la oferta dada de baja sigue")
	}
	// El producto sigue existiendo: lista vacía, no ErrProductoNoEncontrado.
	if _, err := repo.OfertasPorProducto(context.Background(), "FAV-b1"); err != nil {
		t.Errorf("err = %v", err)
	}
	if _, ok := ofertaFAV(t, repo, "FAV-b2"); !ok {
		t.Error("la otra oferta no tenía que tocarse")
	}
}

func TestCompletaBorraLoQueNoVinoYSoloDeEseProveedor(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	aplicar(t, repo, cambio(false, []string{"FAV-c1", "FAV-c2"}, map[string]float64{"FAV-c1": 100, "FAV-c2": 200}, nil))
	antes, err := repo.OfertasPorProducto(context.Background(), "PAN-550W-MONO")
	if err != nil {
		t.Fatal(err)
	}

	res := aplicar(t, repo, cambio(true, nil, map[string]float64{"FAV-c1": 150}, nil))
	if !slices.Equal(res.SKUs, []string{"FAV-c1", "FAV-c2"}) {
		t.Errorf("tocados = %v: la oferta borrada también hay que invalidarla", res.SKUs)
	}
	if o, ok := ofertaFAV(t, repo, "FAV-c1"); !ok || o.PrecioUnitario != 150 {
		t.Errorf("oferta = %+v, %v", o, ok)
	}
	if _, ok := ofertaFAV(t, repo, "FAV-c2"); ok {
		t.Error("la que no vino sigue")
	}
	despues, err := repo.OfertasPorProducto(context.Background(), "PAN-550W-MONO")
	if err != nil {
		t.Fatal(err)
	}
	if len(despues) != len(antes) {
		t.Errorf("ofertas de otros proveedores: %d → %d", len(antes), len(despues))
	}
}

// Es el evento de catalogo_vencido: completa sin ninguna oferta. Con un array
// NULL en la consulta no borraría nada.
func TestCompletaVaciaRetiraTodoElCatalogoDelProveedor(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	aplicar(t, repo, cambio(false, []string{"FAV-v1", "FAV-v2"}, map[string]float64{"FAV-v1": 100, "FAV-v2": 200}, nil))
	res := aplicar(t, repo, cambio(true, nil, nil, nil))
	if !res.Aplicado || !slices.Equal(res.SKUs, []string{"FAV-v1", "FAV-v2"}) {
		t.Fatalf("resultado = %+v", res)
	}
	for _, sku := range []string{"FAV-v1", "FAV-v2"} {
		if _, ok := ofertaFAV(t, repo, sku); ok {
			t.Errorf("%s sigue en el ranking", sku)
		}
	}
}

func TestUnSKURepetidoEnElEventoNoRompeLaTransaccion(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	c := cambio(false, []string{"FAV-r", "FAV-r"}, nil, nil)
	c.Ofertas = []ports.OfertaCambio{
		{ProductoSKU: "FAV-r", PrecioUnitario: 1, StockDisponible: 1},
		{ProductoSKU: "FAV-r", PrecioUnitario: 2, StockDisponible: 1},
	}
	aplicar(t, repo, c)
	if o, _ := ofertaFAV(t, repo, "FAV-r"); o.PrecioUnitario != 2 {
		t.Errorf("precio = %v, quiero el último", o.PrecioUnitario)
	}
}

func TestSiFallaAlgoNoQuedaNadaAplicado(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)

	// La oferta apunta a un producto que no existe: la clave foránea hace fallar
	// la transacción después de haber escrito el producto y la secuencia.
	c := cambio(false, []string{"FAV-rollback"}, map[string]float64{"FAV-no-existe": 100}, nil)
	if _, err := repo.AplicarListaPrecios(context.Background(), c); err == nil {
		t.Fatal("tenía que fallar")
	}
	if _, err := repo.OfertasPorProducto(context.Background(), "FAV-rollback"); !errors.Is(err, domain.ErrProductoNoEncontrado) {
		t.Errorf("el producto quedó escrito: err = %v", err)
	}
	var eventos int
	if err := poolCompartido.QueryRow(context.Background(),
		`SELECT count(*) FROM eventos_procesados WHERE evento_id = $1`, c.EventoID).Scan(&eventos); err != nil {
		t.Fatal(err)
	}
	if eventos != 0 {
		t.Error("el evento quedó marcado como procesado: el reintento se descartaría como duplicado")
	}
	pendientes, err := repo.SKUsPendientes(context.Background(), 100)
	if err != nil || len(pendientes) != 0 {
		t.Errorf("pendientes = %v, err = %v", pendientes, err)
	}

	// El reintento con el dato corregido se aplica: la secuencia tampoco avanzó.
	corregido := c
	corregido.Ofertas = []ports.OfertaCambio{{ProductoSKU: "FAV-rollback", PrecioUnitario: 7, StockDisponible: 1}}
	if res := aplicar(t, repo, corregido); !res.Aplicado {
		t.Errorf("resultado = %+v", res)
	}
}

func TestLosSKUTocadosQuedanPendientesYUnCambioNuevoSubeLaVersion(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)
	ctx := context.Background()

	aplicar(t, repo, cambio(false, []string{"FAV-p"}, map[string]float64{"FAV-p": 1}, nil))
	leidos, err := repo.SKUsPendientes(ctx, 10)
	if err != nil || len(leidos) != 1 || leidos[0].SKU != "FAV-p" {
		t.Fatalf("pendientes = %v, err = %v", leidos, err)
	}

	// Cambia mientras se indexaba la versión leída: no se puede perder.
	aplicar(t, repo, cambio(false, nil, map[string]float64{"FAV-p": 2}, nil))
	if err := repo.QuitarPendientes(ctx, leidos); err != nil {
		t.Fatal(err)
	}
	quedan, err := repo.SKUsPendientes(ctx, 10)
	if err != nil || len(quedan) != 1 || quedan[0].Version != leidos[0].Version+1 {
		t.Fatalf("quedan = %v, err = %v: el segundo cambio se perdió", quedan, err)
	}

	// Sin cambios en el medio, se borra.
	if err := repo.QuitarPendientes(ctx, quedan); err != nil {
		t.Fatal(err)
	}
	if quedan, _ := repo.SKUsPendientes(ctx, 10); len(quedan) != 0 {
		t.Errorf("quedan = %v", quedan)
	}
}

func TestDocumentoConAgregadosSoloDeOfertasConStock(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)
	ctx := context.Background()

	// Es el ejemplo de ADR-006: el proveedor 1 tiene el precio más bajo pero stock 0.
	docs, err := repo.DocumentosDeProductos(ctx, []string{"PAN-550W-MONO", "NO-EXISTE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("documentos = %+v", docs)
	}
	d := docs[0]
	if d.PrecioMin == nil || *d.PrecioMin != 189573.99 {
		t.Errorf("precio_min = %v, quiero 189573.99", d.PrecioMin)
	}
	if d.ProveedoresConStock != 9 || len(d.Proveedores) != 9 {
		t.Errorf("con stock = %d, proveedores = %v", d.ProveedoresConStock, d.Proveedores)
	}
	if slices.Contains(d.Proveedores, "Andes Solar SRL") {
		t.Errorf("el que no tiene stock no va en la lista: %v", d.Proveedores)
	}
	if !slices.IsSorted(d.Proveedores) {
		t.Errorf("sin orden fijo, reindexar no sería idempotente: %v", d.Proveedores)
	}
	if d.StockTotal != 1667 || d.Nombre != "Panel solar 550 W monocristalino" || d.Categoria != "paneles" || d.Unidad != "unidad" {
		t.Errorf("documento = %+v", d)
	}
	if d.ActualizadoEn.IsZero() {
		t.Error("falta actualizado_en")
	}
}

func TestDocumentoDeProductoSinOfertasNiStock(t *testing.T) {
	repo := migrarCompartido(t)
	limpiarFAV(t)
	ctx := context.Background()

	c := cambio(false, []string{"FAV-sin-stock", "FAV-sin-ofertas"}, nil, nil)
	c.Ofertas = []ports.OfertaCambio{{ProductoSKU: "FAV-sin-stock", PrecioUnitario: 100, StockDisponible: 0}}
	aplicar(t, repo, c)

	docs, err := repo.DocumentosDeProductos(ctx, []string{"FAV-sin-stock", "FAV-sin-ofertas"})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("documentos = %+v", docs)
	}
	for _, d := range docs {
		if d.PrecioMin != nil || d.ProveedoresConStock != 0 || d.StockTotal != 0 || d.Proveedores == nil || len(d.Proveedores) != 0 {
			t.Errorf("%s = %+v", d.SKU, d)
		}
		if d.ActualizadoEn.IsZero() {
			t.Errorf("%s sin actualizado_en", d.SKU)
		}
	}
}

func TestTodosLosDocumentosTraeCadaProducto(t *testing.T) {
	repo := migrarCompartido(t)
	docs, err := repo.TodosLosDocumentos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var productos int
	if err := poolCompartido.QueryRow(context.Background(), `SELECT count(*) FROM productos`).Scan(&productos); err != nil {
		t.Fatal(err)
	}
	if len(docs) != productos || productos < 30 {
		t.Errorf("documentos = %d, productos = %d", len(docs), productos)
	}
}

func TestLaExclusionDejaPasarAUnoSolo(t *testing.T) {
	repo := migrarCompartido(t)
	ctx := context.Background()

	var adentro bool
	corrio, err := repo.ConExclusionIndexacion(ctx, func(ctx context.Context) error {
		otra, err := repo.ConExclusionIndexacion(ctx, func(context.Context) error {
			t.Error("entraron dos a la vez")
			return nil
		})
		if err != nil {
			t.Errorf("la segunda: %v", err)
		}
		adentro = !otra
		return nil
	})
	if err != nil || !corrio || !adentro {
		t.Fatalf("corrió = %v, adentro = %v, err = %v", corrio, adentro, err)
	}

	// Al terminar se suelta, también si fn falla.
	falla := errors.New("falla de fn")
	if _, err := repo.ConExclusionIndexacion(ctx, func(context.Context) error { return falla }); !errors.Is(err, falla) {
		t.Errorf("err = %v", err)
	}
	if corrio, err := repo.ConExclusionIndexacion(ctx, func(context.Context) error { return nil }); err != nil || !corrio {
		t.Errorf("no se soltó: corrió = %v, err = %v", corrio, err)
	}
}
