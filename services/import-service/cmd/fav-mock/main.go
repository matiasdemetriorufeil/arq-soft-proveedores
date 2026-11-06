// Command fav-mock levanta el mock del catálogo de FAV (internal/favmock) para
// desarrollo local y para el Game Day. Cambiar el modo de falla:
//
//	curl -X POST localhost:8090/_mock/modo -d '{"modo":"caido"}'
//
// Modos: normal, caido, lento, limite, error, invalido.
//
// Pedidos (los que hace stock-service): simular que FAV verificó el pago, o
// que venció, y enviar el aviso firmado a la notificacionUrl del pedido:
//
//	curl -X POST localhost:8090/_mock/pedidos/<pedidoId>/estado -d '{"estado":"PAGADO"}'
//
// FAV_MOCK_SECRETO_AVISOS es el secreto con el que se firman los avisos (el
// mismo que FAV_SECRETO_AVISOS de stock-service); por defecto, el del contrato.
package main

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/observabilidad"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/favmock"
)

func main() {
	log := observabilidad.NuevoLogger("fav-mock")
	clave := strings.TrimSpace(os.Getenv("FAV_MOCK_CLAVE"))
	if clave == "" {
		log.Error("FAV_MOCK_CLAVE es obligatoria")
		os.Exit(1)
	}
	puerto := strings.TrimSpace(os.Getenv("PORT"))
	if puerto == "" {
		puerto = "8090"
	}
	m, err := favmock.Nuevo(clave)
	if err != nil {
		log.Error("no se pudo armar el mock", "error", err.Error())
		os.Exit(1)
	}
	if s := strings.TrimSpace(os.Getenv("FAV_MOCK_SECRETO_AVISOS")); s != "" {
		m.UsarSecretoDeAvisos(s)
	}
	srv := &http.Server{
		Addr:              ":" + puerto,
		Handler:           m.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("mock de FAV escuchando", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("el mock terminó", "error", err.Error())
		os.Exit(1)
	}
}
