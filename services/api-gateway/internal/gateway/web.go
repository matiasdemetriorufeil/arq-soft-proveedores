package gateway

import (
	"embed"
	"io/fs"
	"net/http"
)

// web es el frontend: HTML, CSS y JS sin build ni framework, embebidos en el
// binario del gateway. Se sirve desde el mismo origen que la API, así que no
// hace falta CORS, y el navegador solo le habla al gateway (nunca a FAV ni a
// un servicio interno).
//
//go:embed web
var web embed.FS

func registrarFrontend(mux *http.ServeMux) {
	estaticos, err := fs.Sub(web, "web")
	if err != nil {
		panic(err) // el embed se resuelve al compilar: no puede fallar en runtime
	}
	archivos := http.FileServerFS(estaticos)
	mux.Handle("GET /web/", http.StripPrefix("/web/", sinCache(archivos)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, estaticos, "index.html")
	})
}

// sinCache evita que en la demo quede pegada una versión vieja del JS después
// de reconstruir la imagen.
func sinCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}
