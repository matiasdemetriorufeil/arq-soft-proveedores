package fav

import (
	"context"
	"time"
)

// ConEsperas deja que los tests corran sin dormir de verdad y registren las
// esperas pedidas, y acorta el timeout por intento.
func (c *Cliente) ConEsperas(timeout time.Duration, registrar func(time.Duration)) *Cliente {
	c.timeout = timeout
	c.dormir = func(_ context.Context, d time.Duration) error {
		registrar(d)
		return nil
	}
	return c
}
