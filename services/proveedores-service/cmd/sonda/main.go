// Command sonda es el healthcheck del contenedor de proveedores-service: la
// imagen distroless no trae shell ni cliente HTTP, así que la sonda tiene que
// ser otro binario de la misma imagen. Sale con 0 si /readyz devuelve 200 y con
// 1 en cualquier otro caso, que es lo que Docker interpreta como "unhealthy".
//
// Es un duplicado del de api-gateway: son dos módulos distintos y cada imagen
// tiene que poder construirse sola, sin depender del otro servicio.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const timeoutSonda = 3 * time.Second

func main() {
	puerto := strings.TrimSpace(os.Getenv("PORT"))
	if puerto == "" {
		puerto = "8080"
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutSonda)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://127.0.0.1:"+puerto+"/readyz", nil)
	if err != nil {
		salirConError(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		salirConError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		salirConError(fmt.Errorf("/readyz devolvió %d", resp.StatusCode))
	}
}

func salirConError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
