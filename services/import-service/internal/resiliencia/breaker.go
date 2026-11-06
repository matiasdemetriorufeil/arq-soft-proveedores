// Package resiliencia tiene el circuit breaker que protege las llamadas a FAV
// (ADR-010). Es chico a propósito: solo los tres estados clásicos, sin
// ventanas deslizantes, porque la llamada que protege corre cada 5 minutos.
package resiliencia

import (
	"errors"
	"sync"
	"time"
)

// Estado del breaker.
type Estado string

const (
	// Cerrado: las llamadas pasan.
	Cerrado Estado = "cerrado"
	// Abierto: las llamadas fallan sin intentarse hasta que pase la espera.
	Abierto Estado = "abierto"
	// SemiAbierto: pasa una sola llamada de prueba; si sale bien se cierra.
	SemiAbierto Estado = "semi_abierto"
)

// ErrAbierto lo devuelve Ejecutar sin llamar a la dependencia.
var ErrAbierto = errors.New("circuit breaker abierto: no se llama a la dependencia")

// Breaker abre el circuito tras FallasParaAbrir fallas seguidas y lo deja
// abierto durante Espera.
type Breaker struct {
	fallasParaAbrir int
	espera          time.Duration
	ahora           func() time.Time

	mu        sync.Mutex
	estado    Estado
	fallas    int
	abiertoEn time.Time
	probando  bool
}

// NuevoBreaker crea un breaker cerrado. ahora puede ser nil (usa time.Now).
func NuevoBreaker(fallasParaAbrir int, espera time.Duration, ahora func() time.Time) *Breaker {
	if ahora == nil {
		ahora = time.Now
	}
	return &Breaker{fallasParaAbrir: fallasParaAbrir, espera: espera, ahora: ahora, estado: Cerrado}
}

// Estado devuelve el estado actual, pasando de abierto a semiabierto si ya
// venció la espera.
func (b *Breaker) Estado() Estado {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actualizarLocked()
	return b.estado
}

func (b *Breaker) actualizarLocked() {
	if b.estado == Abierto && b.ahora().Sub(b.abiertoEn) >= b.espera {
		b.estado = SemiAbierto
		b.probando = false
	}
}

// Ejecutar corre f si el circuito lo permite y registra el resultado. Un error
// que cumpla noCuenta (por ejemplo, una clave inválida, que no es una caída)
// no suma como falla.
func (b *Breaker) Ejecutar(f func() error, noCuenta func(error) bool) error {
	b.mu.Lock()
	b.actualizarLocked()
	switch b.estado {
	case Abierto:
		b.mu.Unlock()
		return ErrAbierto
	case SemiAbierto:
		if b.probando {
			b.mu.Unlock()
			return ErrAbierto
		}
		b.probando = true
	}
	b.mu.Unlock()

	err := f()

	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil || (noCuenta != nil && noCuenta(err)) {
		b.estado, b.fallas, b.probando = Cerrado, 0, false
		return err
	}
	b.fallas++
	if b.estado == SemiAbierto || b.fallas >= b.fallasParaAbrir {
		b.estado, b.abiertoEn, b.probando = Abierto, b.ahora(), false
	}
	return err
}
