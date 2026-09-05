package identity

import "testing"

func TestCymruName(t *testing.T) {
	cases := map[string]string{
		"CORBINA-AS - PJSC _Vimpelcom_, RU":  "PJSC Vimpelcom",
		"AEZA-AS - AEZA GROUP LLC, RU":       "AEZA GROUP LLC",
		"NetCraftersOU - NetCrafters OU, EE": "NetCrafters OU",
		"MHost-AS - MHost LLC, GE":           "MHost LLC",
		"SOMEAS, US":                         "SOMEAS",
		"  spaced   out  ":                   "spaced out",
	}
	for in, want := range cases {
		if got := cymruName(in); got != want {
			t.Errorf("cymruName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChangedComparesNumbersOnly(t *testing.T) {
	a := Identity{ASN: "AS8402 PJSC Vimpelcom"}
	b := Identity{ASN: "AS8402 CORBINA-AS - PJSC _Vimpelcom_, RU"}
	if Changed(a, b) {
		t.Error("same AS number spelled differently must not count as a change")
	}
	if !Changed(a, Identity{ASN: "AS203273 NetCrafters OU"}) {
		t.Error("a different AS number is a change")
	}
}
