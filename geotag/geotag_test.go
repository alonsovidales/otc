package geotag

import "testing"

func TestReverseGeocodeKnownCity(t *testing.T) {
	// Statue of Liberty, NYC — well inside cities15000's coverage.
	city, country, ok := ReverseGeocode(40.6892, -74.0445)
	if !ok {
		t.Fatal("expected a match near New York City")
	}
	if country != "United States" {
		t.Errorf("expected country 'United States', got %q", country)
	}
	if city == "" {
		t.Error("expected a non-empty city name")
	}
}

func TestReverseGeocodeMiddleOfOcean(t *testing.T) {
	// Deep South Pacific, nowhere near any city in the dataset.
	_, _, ok := ReverseGeocode(-48.0, -123.0)
	if ok {
		t.Error("expected no match far out in the ocean")
	}
}

func TestReverseGeocodeEurope(t *testing.T) {
	// Tuscany, Italy — the same coordinates as the GPS EXIF test fixture
	// used to validate the image pipeline end-to-end (resolves to whichever
	// dataset city is actually nearest, e.g. Arezzo rather than Florence).
	city, country, ok := ReverseGeocode(43.4674, 11.8851)
	if !ok {
		t.Fatal("expected a match somewhere in Tuscany")
	}
	if country != "Italy" {
		t.Errorf("expected country 'Italy', got %q", country)
	}
	t.Logf("resolved to %s, %s", city, country)
}
