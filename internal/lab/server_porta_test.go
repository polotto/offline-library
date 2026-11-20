package lab

import "testing"

func TestComPorta(t *testing.T) {
	casos := map[string]string{
		comPorta("192.168.0.5", 80):   "192.168.0.5",
		comPorta("192.168.0.5", 8087): "192.168.0.5:8087",
		comPorta("lab-0.local", 80):   "lab-0.local",
	}
	for obtido, esperado := range casos {
		if obtido != esperado {
			t.Errorf("obtido %q, esperado %q", obtido, esperado)
		}
	}
}
