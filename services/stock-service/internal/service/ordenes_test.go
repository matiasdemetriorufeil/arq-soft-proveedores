package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/proveedores"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// repoOrdenesFalso es el doble de la capa de datos para las órdenes: guarda
// todo en memoria, sin base ni índice único. Lo que se prueba acá es la regla
// (RN-15 y el ciclo), no el esquema: el índice único parcial lo prueban los
// tests de integración de internal/repository.
type repoOrdenesFalso struct {
	bajoMinimo []repository.Stock
	ordenes    map[string]*repository.Orden
	abiertas   map[string]bool // por SKU

	errBajoMinimo error
	errAbierta    error
	errCrear      error
	errLeer       error
	// estadoCambiado simula que otra request movió la orden entre la lectura y
	// el UPDATE: ActualizarEstadoOrden no encuentra la fila en `desde`.
	estadoCambiado string

	creadas []repository.Orden
}

func nuevoRepoOrdenesFalso() *repoOrdenesFalso {
	return &repoOrdenesFalso{
		ordenes:  map[string]*repository.Orden{},
		abiertas: map[string]bool{},
	}
}

func (f *repoOrdenesFalso) SKUsBajoMinimo(context.Context) ([]repository.Stock, error) {
	if f.errBajoMinimo != nil {
		return nil, f.errBajoMinimo
	}
	return f.bajoMinimo, nil
}

func (f *repoOrdenesFalso) OrdenAbiertaPorSKU(_ context.Context, sku string) (bool, error) {
	if f.errAbierta != nil {
		return false, f.errAbierta
	}
	return f.abiertas[sku], nil
}

func (f *repoOrdenesFalso) CrearOrden(_ context.Context, o *repository.Orden) error {
	if f.errCrear != nil {
		return f.errCrear
	}
	if f.abiertas[o.SKU] {
		return repository.ErrOrdenAbiertaExistente
	}
	copia := *o
	f.ordenes[o.ID] = &copia
	f.abiertas[o.SKU] = true
	f.creadas = append(f.creadas, copia)
	return nil
}

func (f *repoOrdenesFalso) OrdenPorID(_ context.Context, id string) (repository.Orden, error) {
	if f.errLeer != nil {
		return repository.Orden{}, f.errLeer
	}
	o, ok := f.ordenes[id]
	if !ok {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	return *o, nil
}

func (f *repoOrdenesFalso) OrdenesPorEstado(_ context.Context, estado string) ([]repository.Orden, error) {
	if f.errLeer != nil {
		return nil, f.errLeer
	}
	lista := make([]repository.Orden, 0, len(f.ordenes))
	for _, o := range f.ordenes {
		if estado == "" || o.Estado == estado {
			lista = append(lista, *o)
		}
	}
	return lista, nil
}

func (f *repoOrdenesFalso) ActualizarEstadoOrden(_ context.Context, id, desde, hacia string) (repository.Orden, error) {
	o, ok := f.ordenes[id]
	if !ok {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	if f.estadoCambiado != "" {
		// La carrera: la fila ya no está en `desde`.
		o.Estado = f.estadoCambiado
		f.estadoCambiado = ""
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	if o.Estado != desde {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	o.Estado = hacia
	return *o, nil
}

// conOrden deja una orden en el estado pedido y devuelve su id.
func (f *repoOrdenesFalso) conOrden(id, sku, estado string) string {
	f.ordenes[id] = &repository.Orden{ID: id, SKU: sku, Cantidad: 10, Estado: estado}
	if estado != service.EstadoConfirmada {
		f.abiertas[sku] = true
	}
	return id
}

// clienteFalso es el doble de proveedores-service: ni HTTP ni reintentos acá.
type clienteFalso struct {
	rec      proveedores.Recomendacion
	err      error
	llamadas []string
}

func (c *clienteFalso) MasBarato(_ context.Context, sku string, _ int) (proveedores.Recomendacion, error) {
	c.llamadas = append(c.llamadas, sku)
	if c.err != nil {
		return proveedores.Recomendacion{}, c.err
	}
	return c.rec, nil
}

// TestTransicionesDeLaOrden recorre las 25 combinaciones de los cinco estados
// de RN-15: las tres filas de transiciones permitidas y todas las demás, que
// tienen que rechazarse (CA-14.3). Incluye explícitamente las que RN-15
// prohíbe nombrándolas: saltear SOLICITADA (BORRADOR → CONFIRMADA) y volver
// para atrás.
func TestTransicionesDeLaOrden(t *testing.T) {
	estados := service.EstadosValidos()

	// Lo único permitido por RN-15.
	permitidas := map[string]bool{
		service.EstadoBorrador + "→" + service.EstadoSolicitada:   true,
		service.EstadoSolicitada + "→" + service.EstadoConfirmada: true,
		service.EstadoSolicitada + "→" + service.EstadoVencida:    true,
		service.EstadoSolicitada + "→" + service.EstadoEnRevision: true,
		service.EstadoVencida + "→" + service.EstadoEnRevision:    true,
	}

	for _, desde := range estados {
		for _, hacia := range estados {
			nombre := desde + "→" + hacia
			t.Run(nombre, func(t *testing.T) {
				repo := nuevoRepoOrdenesFalso()
				id := repo.conOrden("orden-1", "SKU-1", desde)
				svc := service.NuevoServicioOrdenes(repo, nil)

				orden, err := svc.TransicionarOrden(context.Background(), id, hacia)

				if permitidas[nombre] {
					if err != nil {
						t.Fatalf("%s tendría que estar permitida; error = %v", nombre, err)
					}
					if orden.Estado != hacia {
						t.Errorf("estado = %q; se esperaba %q", orden.Estado, hacia)
					}
					return
				}

				if !errors.Is(err, service.ErrTransicionInvalida) {
					t.Fatalf("%s tendría que rechazarse con ErrTransicionInvalida; error = %v", nombre, err)
				}
				// La orden no cambió (CA-14.3).
				quedo, errLectura := repo.OrdenPorID(context.Background(), id)
				if errLectura != nil {
					t.Fatalf("releer la orden: %v", errLectura)
				}
				if quedo.Estado != desde {
					t.Errorf("la orden quedó en %q; tendría que seguir en %q", quedo.Estado, desde)
				}
				var invalida *service.TransicionInvalidaError
				if !errors.As(err, &invalida) || invalida.Desde != desde || invalida.Hacia != hacia {
					t.Errorf("el error no lleva el detalle de la transición: %v", err)
				}
			})
		}
	}
}

// TestTransicionConEstadoDesconocido: un estado fuera de los cinco de RN-15 no
// es una transición inválida, es un parámetro inválido (400, no 409).
func TestTransicionConEstadoDesconocido(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	id := repo.conOrden("orden-1", "SKU-1", service.EstadoBorrador)
	svc := service.NuevoServicioOrdenes(repo, nil)

	for _, estado := range []string{"", "   ", "ENVIADA", "borrador", "CANCELADA"} {
		if _, err := svc.TransicionarOrden(context.Background(), id, estado); !errors.Is(err, service.ErrParametroInvalido) {
			t.Errorf("estado %q: error = %v; se esperaba ErrParametroInvalido", estado, err)
		}
	}
}

func TestTransicionDeOrdenInexistente(t *testing.T) {
	svc := service.NuevoServicioOrdenes(nuevoRepoOrdenesFalso(), nil)

	_, err := svc.TransicionarOrden(context.Background(), "no-existe", service.EstadoSolicitada)
	if !errors.Is(err, service.ErrOrdenNoEncontrada) {
		t.Errorf("error = %v; se esperaba ErrOrdenNoEncontrada", err)
	}
}

// TestTransicionPierdeLaCarrera: dos requests piden la misma transición a la
// vez. El UPDATE del que pierde no encuentra la fila en el estado que leyó, así
// que se rechaza con el estado nuevo en lugar de pisarlo.
func TestTransicionPierdeLaCarrera(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	id := repo.conOrden("orden-1", "SKU-1", service.EstadoBorrador)
	repo.estadoCambiado = service.EstadoSolicitada
	svc := service.NuevoServicioOrdenes(repo, nil)

	_, err := svc.TransicionarOrden(context.Background(), id, service.EstadoSolicitada)
	if !errors.Is(err, service.ErrTransicionInvalida) {
		t.Fatalf("error = %v; se esperaba ErrTransicionInvalida", err)
	}
	quedo, _ := repo.OrdenPorID(context.Background(), id)
	if quedo.Estado != service.EstadoSolicitada {
		t.Errorf("la orden quedó en %q; el ganador la había dejado en SOLICITADA", quedo.Estado)
	}
}

func TestOrdenesFiltraPorEstado(t *testing.T) {
	repo := nuevoRepoOrdenesFalso()
	repo.conOrden("o-1", "SKU-1", service.EstadoBorrador)
	repo.conOrden("o-2", "SKU-2", service.EstadoSolicitada)
	svc := service.NuevoServicioOrdenes(repo, nil)
	ctx := context.Background()

	todas, err := svc.Ordenes(ctx, "")
	if err != nil || len(todas) != 2 {
		t.Fatalf("Ordenes(\"\") = %d órdenes, %v; se esperaban 2 sin error", len(todas), err)
	}

	borradores, err := svc.Ordenes(ctx, service.EstadoBorrador)
	if err != nil || len(borradores) != 1 {
		t.Fatalf("Ordenes(BORRADOR) = %d órdenes, %v; se esperaba 1 sin error", len(borradores), err)
	}

	if _, err := svc.Ordenes(ctx, "INVENTADO"); !errors.Is(err, service.ErrParametroInvalido) {
		t.Errorf("un estado que no existe tiene que ser ErrParametroInvalido; error = %v", err)
	}
}
