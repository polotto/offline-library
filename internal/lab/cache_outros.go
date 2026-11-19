//go:build !linux

package lab

// esquecerCache não tem equivalente simples fora do Linux; a conferência
// de integridade do SQLite depois da cópia cobre o caso.
func esquecerCache(string) {}
