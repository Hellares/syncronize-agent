package config

import "testing"

func TestLeeElFormatoViejo(t *testing.T) {
	// config.json de v0.2-v0.4: un solo vínculo en la raíz.
	c, err := Decodificar([]byte(`{"servidor":"https://saas-beta.syncronize.net.pe/api","agenteId":"ag1","token":"sag_x","intervaloSegundos":900,"continuo":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Vinculos) != 1 || c.Vinculos[0].AgenteID != "ag1" || c.Vinculos[0].Token != "sag_x" || !c.Vinculos[0].Continuo {
		t.Fatalf("%+v", c.Vinculos)
	}
	// Uno viejo revocado = sin vínculos.
	c, _ = Decodificar([]byte(`{"servidor":"s","agenteId":"ag1","token":"t","revocado":true}`))
	if len(c.Vinculos) != 0 {
		t.Fatalf("revocado no debe quedar: %+v", c.Vinculos)
	}
}

func TestVariosServiciosTecnicos(t *testing.T) {
	c := &Config{}
	tallerA := &Vinculo{Servidor: ServidorProd, AgenteID: "ag-c", Token: "t1", Empresa: "TALLER A"}
	tallerB := &Vinculo{Servidor: ServidorProd, AgenteID: "ag-s", Token: "t2", Empresa: "TALLER B"}
	c.Agregar(tallerA)
	c.Agregar(tallerB)
	// El taller A vuelve a vincular el mismo equipo (el backend devuelve el mismo
	// agenteId con un token nuevo): reemplaza, no duplica.
	c.Agregar(&Vinculo{Servidor: ServidorProd + "/", AgenteID: "ag-c", Token: "t3", Empresa: "TALLER A"})
	if len(c.Vinculos) != 2 || c.Vinculos[0].Token != "t3" || c.Vinculos[1] != tallerB {
		t.Fatalf("%+v", c.Vinculos)
	}
	// El taller B lo desvincula: el A sigue.
	c.Quitar(tallerB)
	if len(c.Vinculos) != 1 || c.Vinculos[0].Empresa != "TALLER A" {
		t.Fatalf("%+v", c.Vinculos)
	}

	// Ida y vuelta por JSON con el formato nuevo.
	b := []byte(`{"vinculos":[{"servidor":"s","agenteId":"a","token":"t","empresa":"TALLER A"},{"servidor":"","agenteId":"roto","token":""}]}`)
	c2, err := Decodificar(b)
	if err != nil || len(c2.Vinculos) != 1 || c2.Vinculos[0].Nombre() != "TALLER A" {
		t.Fatalf("%+v %v", c2, err)
	}
	if (&Vinculo{AgenteID: "x"}).Nombre() != "servicio técnico x" {
		t.Fatal("nombre por defecto")
	}
}
