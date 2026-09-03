// Package geotag turns a GPS coordinate into a city/country name entirely
// offline (issue #42) — no third-party geocoding API, no network call, and
// no GPS coordinates ever leave the device. It works from a bundled,
// trimmed extract of the GeoNames "cities15000" dataset (cities with a
// population of 15,000+, https://download.geonames.org/export/dump/,
// CC BY 4.0) plus GeoNames' countryInfo.txt for the country-code-to-name
// mapping, both embedded at compile time.
package geotag

import (
	_ "embed"
	"math"
	"strconv"
	"strings"
)

//go:embed data/cities.tsv
var citiesData string

//go:embed data/countries.tsv
var countriesData string

type city struct {
	name        string
	lat, lon    float64
	countryCode string
}

var (
	cities    []city
	countries map[string]string
)

func init() {
	for _, line := range strings.Split(strings.TrimSpace(citiesData), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		lat, errLat := strconv.ParseFloat(f[1], 64)
		lon, errLon := strconv.ParseFloat(f[2], 64)
		if errLat != nil || errLon != nil {
			continue
		}
		cities = append(cities, city{name: f[0], lat: lat, lon: lon, countryCode: f[3]})
	}

	countries = make(map[string]string, 260)
	for _, line := range strings.Split(strings.TrimSpace(countriesData), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		countries[f[0]] = f[1]
	}
}

// maxDistanceKm caps how far away the nearest known city can be before we
// give up rather than mislabel a remote area (open ocean, polar regions,
// sparsely populated interiors) with a "nearest" city that isn't actually
// close.
const maxDistanceKm = 300

// ReverseGeocode returns the nearest known city and its country for a GPS
// coordinate. ok is false if the dataset has nothing within maxDistanceKm.
func ReverseGeocode(lat, lon float64) (cityName, country string, ok bool) {
	if len(cities) == 0 {
		return "", "", false
	}
	best := -1
	bestDist := math.MaxFloat64
	for i, c := range cities {
		d := haversineKm(lat, lon, c.lat, c.lon)
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	if best == -1 || bestDist > maxDistanceKm {
		return "", "", false
	}
	match := cities[best]
	countryName := countries[match.countryCode]
	if countryName == "" {
		countryName = match.countryCode
	}
	return match.name, countryName, true
}

// haversineKm is the great-circle distance between two coordinates, in km.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}
