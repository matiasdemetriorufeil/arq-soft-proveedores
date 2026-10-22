package domain

import (
	"errors"
	"fmt"
	"testing"
)

func oferta(id int64, nombre string, precio, desc float64, minDesc int, flete float64, stock int) Oferta {
	return Oferta{
		ProveedorID:             id,
		ProveedorNombre:         nombre,
		ProductoSKU:             "SKU-1",
		PrecioUnitario:          precio,
		DescuentoPct:            desc,
		CantidadMinimaDescuento: minDesc,
		Flete:                   flete,
		StockDisponible:         stock,
	}
}

func TestCostoTotal(t *testing.T) {
	tests := []struct {
		nombre     string
		o          Oferta
		cantidad   int
		want       float64
		wantAplica bool
	}{
		{"sin descuento", oferta(1, "A", 100, 0, 0, 50, 100), 10, 1050, false},
		{"bajo el umbral no aplica", oferta(1, "A", 100, 10, 20, 50, 100), 19, 1950, false},
		{"en el umbral aplica", oferta(1, "A", 100, 10, 20, 50, 100), 20, 1850, true},
		{"sobre el umbral aplica", oferta(1, "A", 100, 10, 20, 0, 100), 30, 2700, true},
		{"redondeo a 2 decimales", oferta(1, "A", 10.333, 0, 0, 0, 100), 3, 31, false},
		{"redondeo de mitad con ruido de float", oferta(1, "A", 189573.99, 15, 5, 43000, 100), 10, 1654378.92, true},
		{"redondeo con descuento", oferta(1, "A", 9.99, 12.5, 1, 0.005, 100), 7, 61.19, true},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			got, aplica := CostoTotal(tt.o, tt.cantidad)
			if got != tt.want || aplica != tt.wantAplica {
				t.Fatalf("CostoTotal() = (%v, %v), want (%v, %v)", got, aplica, tt.want, tt.wantAplica)
			}
		})
	}
}

func ids(recs []Recomendacion) []int64 {
	out := make([]int64, len(recs))
	for i, r := range recs {
		out[i] = r.ProveedorID
	}
	return out
}

func TestRankear(t *testing.T) {
	ocho := make([]Oferta, 0, 8)
	for i := 1; i <= 8; i++ {
		// precio decreciente: el proveedor 8 es el más barato
		ocho = append(ocho, oferta(int64(i), fmt.Sprintf("Prov %d", i), float64(200-i*10), 0, 0, 0, 100))
	}

	tests := []struct {
		nombre   string
		ofertas  []Oferta
		cantidad int
		limite   int
		wantIDs  []int64
	}{
		{
			nombre: "feliz: ordena por costo total asc",
			ofertas: []Oferta{
				oferta(1, "Caro", 120, 0, 0, 0, 50),
				oferta(2, "Barato", 100, 0, 0, 10, 50),
				oferta(3, "Medio", 105, 0, 0, 0, 50),
			},
			cantidad: 10, limite: 5,
			wantIDs: []int64{2, 3, 1}, // 1010, 1050, 1200
		},
		{
			nombre: "umbral de descuento cambia el orden",
			ofertas: []Oferta{
				oferta(1, "Sin desc", 95, 0, 0, 0, 100),
				oferta(2, "Con desc", 100, 10, 10, 0, 100), // a 10 unidades: 900 < 950
			},
			cantidad: 10, limite: 5,
			wantIDs: []int64{2, 1},
		},
		{
			nombre: "un debajo del umbral el descuento no aplica",
			ofertas: []Oferta{
				oferta(1, "Sin desc", 95, 0, 0, 0, 100),
				oferta(2, "Con desc", 100, 10, 10, 0, 100), // a 9: 900 > 855
			},
			cantidad: 9, limite: 5,
			wantIDs: []int64{1, 2},
		},
		{
			nombre: "excluye ofertas sin stock suficiente",
			ofertas: []Oferta{
				oferta(1, "A", 10, 0, 0, 0, 9),
				oferta(2, "B", 50, 0, 0, 0, 10), // stock == cantidad es válido
			},
			cantidad: 10, limite: 5,
			wantIDs: []int64{2},
		},
		{
			nombre: "sin stock suficiente en ninguna: lista vacía",
			ofertas: []Oferta{
				oferta(1, "A", 10, 0, 0, 0, 1),
				oferta(2, "B", 10, 0, 0, 0, 0),
			},
			cantidad: 10, limite: 5,
			wantIDs: []int64{},
		},
		{
			nombre:   "sin ofertas: lista vacía",
			ofertas:  nil,
			cantidad: 1, limite: 5,
			wantIDs: []int64{},
		},
		{
			nombre:   "limite devuelve las más baratas",
			ofertas:  ocho,
			cantidad: 1, limite: 3,
			wantIDs: []int64{8, 7, 6},
		},
		{
			nombre:   "limite máximo",
			ofertas:  ocho,
			cantidad: 1, limite: LimiteMaximo,
			wantIDs: []int64{8, 7, 6, 5, 4, 3, 2, 1},
		},
		{
			nombre: "empate: desempata por nombre",
			ofertas: []Oferta{
				oferta(1, "Zeta", 10, 0, 0, 0, 10),
				oferta(2, "Alfa", 10, 0, 0, 0, 10),
			},
			cantidad: 1, limite: 5,
			wantIDs: []int64{2, 1},
		},
		{
			nombre: "empate de costo y nombre: desempata por id",
			ofertas: []Oferta{
				oferta(7, "Igual", 10, 0, 0, 0, 10),
				oferta(3, "Igual", 10, 0, 0, 0, 10),
			},
			cantidad: 1, limite: 5,
			wantIDs: []int64{3, 7},
		},
		{
			nombre: "empate tras redondear el costo",
			ofertas: []Oferta{
				oferta(1, "B", 10.001, 0, 0, 0, 10),
				oferta(2, "A", 10.004, 0, 0, 0, 10),
			},
			cantidad: 1, limite: 5,
			wantIDs: []int64{2, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			got, err := Rankear(tt.ofertas, tt.cantidad, tt.limite)
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if got == nil {
				t.Fatal("Rankear devolvió nil; se espera lista vacía")
			}
			if fmt.Sprint(ids(got)) != fmt.Sprint(tt.wantIDs) {
				t.Fatalf("ids = %v, want %v", ids(got), tt.wantIDs)
			}
		})
	}
}

func TestRankearCamposRecomendacion(t *testing.T) {
	got, err := Rankear([]Oferta{oferta(4, "Solar SA", 100, 10, 5, 25.5, 40)}, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := Recomendacion{
		ProveedorID: 4, ProveedorNombre: "Solar SA", PrecioUnitario: 100, DescuentoPct: 10,
		DescuentoAplicado: true, Flete: 25.5, StockDisponible: 40, CostoTotal: 475.5,
	}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
}

func TestRankearParametrosInvalidos(t *testing.T) {
	tests := []struct {
		nombre           string
		cantidad, limite int
	}{
		{"cantidad cero", 0, 5},
		{"cantidad negativa", -1, 5},
		{"limite cero", 1, 0},
		{"limite sobre el máximo", 1, LimiteMaximo + 1},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			_, err := Rankear(nil, tt.cantidad, tt.limite)
			if !errors.Is(err, ErrParametroInvalido) {
				t.Fatalf("err = %v, want ErrParametroInvalido", err)
			}
		})
	}
}
