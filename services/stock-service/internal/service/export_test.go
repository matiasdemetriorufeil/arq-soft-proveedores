package service

// ProcesarAvisosEnElMomento hace que RecibirAviso procese el aviso antes de
// volver, para poder mirar el resultado en el test.
func ProcesarAvisosEnElMomento(s *ServicioPedidos) {
	s.enSegundoPlano = func(f func()) { f() }
}
