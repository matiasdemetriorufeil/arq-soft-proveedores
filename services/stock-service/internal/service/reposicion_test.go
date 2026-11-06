package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/proveedores"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

var masBarato = proveedores.Recomendacion{
	ProveedorID:     3,
	ProveedorNombre: "Cordoba Energia SA",
	CostoTotal:      1654378.92,
}

// TestCicloCreaOrdenParaElFaltante es la evidencia de CA-14.1: un SKU bajo el
// mínimo termina con una orden en BORRADOR dirigida al proveedor más barato, y
// con la cantidad que deja el stock en el doble del mínimo.
func TestCicloCreaOrdenParaElFaltante(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	repo.bajoMinimo = []repository.Stock{{SKU: "PROT-DC-1000V", Disponible: 4, Minimo: 10}}
	cliente := &clienteFalso{rec: masBarato}
	svc := service.NuevoServicioOrdenes(repo, cliente)

	res, err := svc.CicloDeReposicion(context.Background())
	if err != nil {
		t.Fatalf("CicloDeReposicion: %v", err)
	}
	if res.Creadas != 1 || res.Salteadas != 0 || res.SinProveedor != 0 || res.Fallidas != 0 {
		t.Fatalf("resumen = %+v; se esperaba 1 creada y nada más", res)
	}
	if len(repo.creadas) != 1 {
		t.Fatalf("se crearon %d órdenes; se esperaba 1", len(repo.creadas))
	}

	o := repo.creadas[0]
	if o.Estado != service.EstadoBorrador {
		t.Errorf("estado = %q; la orden tiene que nacer en BORRADOR", o.Estado)
	}
	// minimo × 2 − disponible = 10 × 2 − 4
	if o.Cantidad != 16 {
		t.Errorf("cantidad = %d; se esperaba 16 (minimo × 2 − disponible)", o.Cantidad)
	}
	if o.ProveedorID == nil || *o.ProveedorID != masBarato.ProveedorID {
		t.Errorf("proveedor_id = %v; se esperaba %d (RN-16)", o.ProveedorID, masBarato.ProveedorID)
	}
	if o.CostoTotal == nil || *o.CostoTotal != masBarato.CostoTotal {
		t.Errorf("costo_total = %v; se esperaba %v", o.CostoTotal, masBarato.CostoTotal)
	}
	if o.Motivo != nil {
		t.Errorf("motivo = %q; una orden con proveedor no lleva motivo", *o.Motivo)
	}
	// La cantidad que se le pregunta al proveedor es la que se va a comprar.
	if len(cliente.llamadas) != 1 || cliente.llamadas[0] != "PROT-DC-1000V" {
		t.Errorf("llamadas al proveedor = %v; se esperaba una por el SKU faltante", cliente.llamadas)
	}
}

// TestCicloSalteaElQueYaTieneOrdenAbierta es la evidencia de CA-14.2 en la capa
// de negocio: no se crea una segunda orden, y no se molesta a
// proveedores-service por un SKU que ya tiene la suya.
func TestCicloSalteaElQueYaTieneOrdenAbierta(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	repo.bajoMinimo = []repository.Stock{{SKU: "PROT-DC-1000V", Disponible: 4, Minimo: 10}}
	repo.conOrden("orden-vieja", "PROT-DC-1000V", service.EstadoBorrador)
	cliente := &clienteFalso{rec: masBarato}
	svc := service.NuevoServicioOrdenes(repo, cliente)

	res, err := svc.CicloDeReposicion(context.Background())
	if err != nil {
		t.Fatalf("CicloDeReposicion: %v", err)
	}
	if res.Creadas != 0 || res.Salteadas != 1 {
		t.Errorf("resumen = %+v; se esperaba 0 creadas y 1 salteada", res)
	}
	if len(cliente.llamadas) != 0 {
		t.Errorf("se llamó al proveedor %d veces; con la orden ya abierta no hace falta", len(cliente.llamadas))
	}
}

// TestCicloIgnoraElStockPorEncimaDelMinimo: el ciclo solo mira lo que el
// repositorio le da como faltante; con la lista vacía no crea nada.
func TestCicloIgnoraElStockPorEncimaDelMinimo(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	cliente := &clienteFalso{rec: masBarato}
	svc := service.NuevoServicioOrdenes(repo, cliente)

	res, err := svc.CicloDeReposicion(context.Background())
	if err != nil {
		t.Fatalf("CicloDeReposicion: %v", err)
	}
	if res.BajoMinimo != 0 || res.Creadas != 0 || len(cliente.llamadas) != 0 {
		t.Errorf("resumen = %+v, llamadas = %v; no tendría que haber hecho nada", res, cliente.llamadas)
	}
}

// TestCicloCreaSinProveedorCuandoFallaLaConsulta: el faltante existe aunque
// proveedores-service no conteste, así que la orden se crea igual, sin
// proveedor y con el motivo registrado.
func TestCicloCreaSinProveedorCuandoFallaLaConsulta(t *testing.T) {
	casos := []struct {
		nombre     string
		err        error
		enElMotivo string
	}{
		{"no responde", proveedores.ErrNoDisponible, "no respondió"},
		{"producto desconocido", proveedores.ErrProductoNoEncontrado, "404"},
		{"nadie tiene stock", proveedores.ErrSinOfertas, "stock suficiente"},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			repo := nuevoRepoOrdenesFalso()
			repo.bajoMinimo = []repository.Stock{{SKU: "INV-10KW-TRI", Disponible: 1, Minimo: 2}}
			svc := service.NuevoServicioOrdenes(repo, &clienteFalso{err: c.err})

			res, err := svc.CicloDeReposicion(context.Background())
			if err != nil {
				t.Fatalf("CicloDeReposicion: %v", err)
			}
			if res.Creadas != 1 || res.SinProveedor != 1 || res.Fallidas != 0 {
				t.Fatalf("resumen = %+v; se esperaba 1 creada sin proveedor", res)
			}
			o := repo.creadas[0]
			if o.ProveedorID != nil {
				t.Errorf("proveedor_id = %v; se esperaba sin proveedor", *o.ProveedorID)
			}
			if o.Motivo == nil || !strings.Contains(*o.Motivo, c.enElMotivo) {
				t.Errorf("motivo = %v; tendría que mencionar %q", o.Motivo, c.enElMotivo)
			}
			if o.Estado != service.EstadoBorrador {
				t.Errorf("estado = %q; se esperaba BORRADOR", o.Estado)
			}
		})
	}
}

// TestCicloSigueConLosDemasSiUnSKUFalla: un SKU que no se puede procesar queda
// contado en el resumen y no corta el ciclo.
func TestCicloSigueConLosDemasSiUnSKUFalla(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	repo.bajoMinimo = []repository.Stock{
		{SKU: "FALLA", Disponible: 1, Minimo: 5},
		{SKU: "ANDA", Disponible: 2, Minimo: 5},
	}
	repo.errCrear = errors.New("la base no responde")
	svc := service.NuevoServicioOrdenes(repo, &clienteFalso{rec: masBarato})

	res, err := svc.CicloDeReposicion(context.Background())
	if err != nil {
		t.Fatalf("CicloDeReposicion: %v; una falla por SKU no tiene que cortar el ciclo", err)
	}
	if res.BajoMinimo != 2 || res.Fallidas != 2 || res.Creadas != 0 {
		t.Errorf("resumen = %+v; se esperaban 2 revisados y 2 fallidos", res)
	}
	if len(res.Problemas) != 2 {
		t.Errorf("problemas = %v; se esperaba uno por SKU", res.Problemas)
	}
}

// TestCicloCuentaLaCarreraComoSalteada: la réplica que pierde contra el índice
// único parcial saltea el SKU, no falla (la fila de la carrera de la matriz).
func TestCicloCuentaLaCarreraComoSalteada(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	repo.bajoMinimo = []repository.Stock{{SKU: "PROT-DC-1000V", Disponible: 4, Minimo: 10}}
	repo.errCrear = repository.ErrOrdenAbiertaExistente
	svc := service.NuevoServicioOrdenes(repo, &clienteFalso{rec: masBarato})

	res, err := svc.CicloDeReposicion(context.Background())
	if err != nil {
		t.Fatalf("CicloDeReposicion: %v", err)
	}
	if res.Salteadas != 1 || res.Creadas != 0 || res.Fallidas != 0 {
		t.Errorf("resumen = %+v; el choque con el índice es una salteada, no un error", res)
	}
}

// TestCicloSinClienteEstaApagado: sin PROVEEDORES_URL el servicio arranca igual
// y el ciclo no corre.
func TestCicloSinClienteEstaApagado(t *testing.T) {
	svc := service.NuevoServicioOrdenes(nuevoRepoOrdenesFalso(), nil)

	if _, err := svc.CicloDeReposicion(context.Background()); !errors.Is(err, service.ErrReposicionDeshabilitada) {
		t.Errorf("error = %v; se esperaba ErrReposicionDeshabilitada", err)
	}
}
