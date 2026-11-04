package main

import (
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/resiliencia"
)

// Con el breaker abierto, las 3 vueltas siguientes no llaman a FAV y la cuarta
// hace la llamada de prueba, aunque cada vuelta tarde un poco en terminar.
func TestElBreakerSalteaTresVueltasYPruebaEnLaCuarta(t *testing.T) {
	for _, intervalo := range []time.Duration{10 * time.Second, 5 * time.Minute} {
		for _, demora := range []time.Duration{0, intervalo / 5} {
			abrio := time.Unix(1000, 0)
			ahora := abrio
			b := resiliencia.NuevoBreaker(fallasParaAbrir, esperaBreaker(intervalo), func() time.Time { return ahora })
			for range fallasParaAbrir {
				b.Ejecutar(func() error { return errPrueba }, nil)
			}
			// La vuelta k arranca k intervalos después de la que falló, que
			// terminó `demora` después de arrancar.
			for k := 1; k <= intervalosAbierto+1; k++ {
				ahora = abrio.Add(time.Duration(k)*intervalo - demora)
				quiero := resiliencia.Abierto
				if k == intervalosAbierto+1 {
					quiero = resiliencia.SemiAbierto
				}
				if got := b.Estado(); got != quiero {
					t.Errorf("intervalo %v, demora %v, vuelta %d: estado = %s, quiero %s", intervalo, demora, k, got, quiero)
				}
			}
		}
	}
}

type errorPrueba struct{}

func (errorPrueba) Error() string { return "caída" }

var errPrueba = errorPrueba{}

func TestCargarConfigExigeLasVariablesEnOrden(t *testing.T) {
	for _, k := range []string{"MONGO_URL", "RABBITMQ_URL", "FAV_URL", "FAV_API_KEY", "INTERVALO_SINCRONIZACION", "FAV_FLETE", "FAV_PROVEEDOR_ID"} {
		t.Setenv(k, "")
	}
	if _, err := cargarConfig(); err == nil || err.Error() != "MONGO_URL es obligatoria" {
		t.Errorf("err = %v", err)
	}
	t.Setenv("MONGO_URL", "mongodb://x")
	t.Setenv("RABBITMQ_URL", "amqp://x")
	t.Setenv("FAV_URL", "http://fav")
	t.Setenv("FAV_API_KEY", "k")
	c, err := cargarConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.intervalo != 5*time.Minute || c.proveedorID != 11 || c.flete != 0 {
		t.Errorf("valores por defecto = %+v", c)
	}
	t.Setenv("INTERVALO_SINCRONIZACION", "5s")
	if _, err := cargarConfig(); err == nil {
		t.Error("un intervalo menor a 10s tenía que fallar")
	}
	t.Setenv("INTERVALO_SINCRONIZACION", "1m")
	t.Setenv("FAV_FLETE", "-1")
	if _, err := cargarConfig(); err == nil {
		t.Error("un flete negativo tenía que fallar")
	}
}
