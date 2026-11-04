package resiliencia

import (
	"errors"
	"testing"
	"time"
)

var errCaida = errors.New("caída")

type reloj struct{ t time.Time }

func (r *reloj) ahora() time.Time { return r.t }

func TestBreakerAbreTrasFallasSeguidasYNoLlama(t *testing.T) {
	r := &reloj{t: time.Unix(0, 0)}
	b := NuevoBreaker(3, 30*time.Second, r.ahora)
	llamadas := 0
	falla := func() error { llamadas++; return errCaida }

	for range 3 {
		if err := b.Ejecutar(falla, nil); !errors.Is(err, errCaida) {
			t.Fatalf("err = %v", err)
		}
	}
	if b.Estado() != Abierto {
		t.Fatalf("estado = %s, quiero abierto", b.Estado())
	}
	if err := b.Ejecutar(falla, nil); !errors.Is(err, ErrAbierto) {
		t.Fatalf("con el circuito abierto err = %v", err)
	}
	if llamadas != 3 {
		t.Errorf("llamó %d veces, quiero 3: abierto no tiene que llamar", llamadas)
	}
}

func TestBreakerUnExitoReiniciaLaCuenta(t *testing.T) {
	b := NuevoBreaker(3, time.Minute, nil)
	b.Ejecutar(func() error { return errCaida }, nil)
	b.Ejecutar(func() error { return errCaida }, nil)
	b.Ejecutar(func() error { return nil }, nil)
	b.Ejecutar(func() error { return errCaida }, nil)
	if b.Estado() != Cerrado {
		t.Errorf("estado = %s: el éxito tenía que reiniciar la cuenta", b.Estado())
	}
}

func TestBreakerSemiAbiertoDejaUnaPruebaYSeCierraSiSaleBien(t *testing.T) {
	r := &reloj{t: time.Unix(0, 0)}
	b := NuevoBreaker(1, 30*time.Second, r.ahora)
	b.Ejecutar(func() error { return errCaida }, nil)
	if b.Estado() != Abierto {
		t.Fatal("no abrió")
	}
	r.t = r.t.Add(30 * time.Second)
	if b.Estado() != SemiAbierto {
		t.Fatalf("estado = %s, quiero semiabierto", b.Estado())
	}

	// Mientras corre la prueba, otra llamada no pasa.
	err := b.Ejecutar(func() error {
		if e := b.Ejecutar(func() error { return nil }, nil); !errors.Is(e, ErrAbierto) {
			t.Errorf("una segunda llamada en semiabierto pasó: %v", e)
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Estado() != Cerrado {
		t.Errorf("estado = %s, quiero cerrado", b.Estado())
	}
}

func TestBreakerSemiAbiertoVuelveAAbrirSiFalla(t *testing.T) {
	r := &reloj{t: time.Unix(0, 0)}
	b := NuevoBreaker(3, 30*time.Second, r.ahora)
	for range 3 {
		b.Ejecutar(func() error { return errCaida }, nil)
	}
	r.t = r.t.Add(31 * time.Second)
	b.Ejecutar(func() error { return errCaida }, nil)
	if b.Estado() != Abierto {
		t.Errorf("estado = %s: una falla en semiabierto tiene que reabrir", b.Estado())
	}
}

func TestBreakerErroresQueNoCuentan(t *testing.T) {
	errClave := errors.New("clave inválida")
	b := NuevoBreaker(1, time.Minute, nil)
	noCuenta := func(err error) bool { return errors.Is(err, errClave) }
	for range 5 {
		b.Ejecutar(func() error { return errClave }, noCuenta)
	}
	if b.Estado() != Cerrado {
		t.Errorf("estado = %s: un 401 no es una caída de FAV", b.Estado())
	}
}
