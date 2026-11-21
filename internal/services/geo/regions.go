// Package geo places provider regions on the map, so a location on one
// provider can be matched with the nearest region of another.
package geo

import (
	"math"
	"sort"
	"strings"

	"microserver/internal/services/providers"
)

// Point is a place on the map.
type Point struct {
	Lat, Lon float64
	Country  string // ISO 3166-1 alpha-2
}

// How a point was found, from most to least exact.
const (
	ByRegion  = "region"  // the provider's own region code
	ByCity    = "city"    // a city name or airport code in the region's id or name
	ByCountry = "country" // the country's main datacenter city
)

func p(lat, lon float64, cc string) Point { return Point{lat, lon, cc} }

// Main datacenter cities, keyed by lower-case city name and IATA code. Used
// for regions the provider tables below do not list (XZone, OVH, new ones).
var cities = map[string]Point{}

func city(pt Point, names ...string) Point {
	for _, n := range names {
		cities[n] = pt
	}
	return pt
}

var (
	frankfurt   = city(p(50.11, 8.68, "DE"), "frankfurt", "fra", "limburg", "lim")
	falkenstein = city(p(50.48, 12.37, "DE"), "falkenstein", "fsn")
	nuremberg   = city(p(49.45, 11.08, "DE"), "nuremberg", "nürnberg", "nue", "nbg")
	berlin      = city(p(52.52, 13.40, "DE"), "berlin", "ber")
	dusseldorf  = city(p(51.23, 6.78, "DE"), "dusseldorf", "düsseldorf", "dus")
	munich      = city(p(48.14, 11.58, "DE"), "munich", "münchen", "muc")
	amsterdam   = city(p(52.37, 4.90, "NL"), "amsterdam", "ams")
	london      = city(p(51.51, -0.13, "GB"), "london", "lhr", "lon")
	manchester  = city(p(53.48, -2.24, "GB"), "manchester", "man")
	paris       = city(p(48.86, 2.35, "FR"), "paris", "cdg", "par")
	gravelines  = city(p(50.99, 2.13, "FR"), "gravelines", "gra")
	strasbourg  = city(p(48.57, 7.75, "FR"), "strasbourg", "sbg")
	roubaix     = city(p(50.69, 3.17, "FR"), "roubaix", "rbx")
	marseille   = city(p(43.30, 5.37, "FR"), "marseille", "mrs")
	helsinki    = city(p(60.17, 24.94, "FI"), "helsinki", "hel")
	stockholm   = city(p(59.33, 18.07, "SE"), "stockholm", "sto", "arn")
	oslo        = city(p(59.91, 10.75, "NO"), "oslo", "osl")
	copenhagen  = city(p(55.68, 12.57, "DK"), "copenhagen", "cph")
	warsaw      = city(p(52.23, 21.01, "PL"), "warsaw", "waw")
	madrid      = city(p(40.42, -3.70, "ES"), "madrid", "mad")
	milan       = city(p(45.46, 9.19, "IT"), "milan", "milano", "mil", "mxp")
	vienna      = city(p(48.21, 16.37, "AT"), "vienna", "vie")
	zurich      = city(p(47.38, 8.54, "CH"), "zurich", "zürich", "zrh")
	brussels    = city(p(50.85, 4.35, "BE"), "brussels", "bru")
	dublin      = city(p(53.35, -6.26, "IE"), "dublin", "dub")
	lisbon      = city(p(38.72, -9.14, "PT"), "lisbon", "lis")
	prague      = city(p(50.08, 14.44, "CZ"), "prague", "prg")
	bucharest   = city(p(44.43, 26.10, "RO"), "bucharest", "otp")
	sofia       = city(p(42.70, 23.32, "BG"), "sofia", "sof")
	budapest    = city(p(47.50, 19.04, "HU"), "budapest", "bud")
	athens      = city(p(37.98, 23.73, "GR"), "athens", "ath")
	istanbul    = city(p(41.01, 28.98, "TR"), "istanbul", "ist")
	moscow      = city(p(55.76, 37.62, "RU"), "moscow", "mow", "svo")
	kyiv        = city(p(50.45, 30.52, "UA"), "kyiv", "kiev", "kbp")
	vilnius     = city(p(54.69, 25.28, "LT"), "vilnius", "vno")
	riga        = city(p(56.95, 24.11, "LV"), "riga", "rix")
	tallinn     = city(p(59.44, 24.75, "EE"), "tallinn", "tll")
	belgrade    = city(p(44.79, 20.45, "RS"), "belgrade", "beg")
	luxembourg  = city(p(49.61, 6.13, "LU"), "luxembourg", "lux")
	telaviv     = city(p(32.09, 34.78, "IL"), "tel aviv", "telaviv", "tlv")
	dubai       = city(p(25.20, 55.27, "AE"), "dubai", "dxb")
	riyadh      = city(p(24.71, 46.68, "SA"), "riyadh", "ruh")
	doha        = city(p(25.29, 51.53, "QA"), "doha", "doh")
	tehran      = city(p(35.69, 51.39, "IR"), "tehran", "thr", "ika")
	tbilisi     = city(p(41.72, 44.79, "GE"), "tbilisi", "tbs")
	yerevan     = city(p(40.18, 44.51, "AM"), "yerevan", "evn")
	baku        = city(p(40.41, 49.87, "AZ"), "baku", "gyd")
	almaty      = city(p(43.24, 76.89, "KZ"), "almaty", "ala")
	mumbai      = city(p(19.08, 72.88, "IN"), "mumbai", "bom")
	delhi       = city(p(28.61, 77.21, "IN"), "delhi", "del")
	bangalore   = city(p(12.97, 77.59, "IN"), "bangalore", "bengaluru", "blr")
	chennai     = city(p(13.08, 80.27, "IN"), "chennai", "maa")
	singapore   = city(p(1.35, 103.82, "SG"), "singapore", "sgp", "sin")
	tokyo       = city(p(35.68, 139.69, "JP"), "tokyo", "nrt", "tyo", "hnd")
	osaka       = city(p(34.69, 135.50, "JP"), "osaka", "itm", "osa", "kix")
	seoul       = city(p(37.57, 126.98, "KR"), "seoul", "icn", "sel")
	hongkong    = city(p(22.32, 114.17, "HK"), "hong kong", "hongkong", "hkg")
	taipei      = city(p(25.03, 121.57, "TW"), "taipei", "tpe")
	jakarta     = city(p(-6.21, 106.85, "ID"), "jakarta", "cgk")
	kualalumpur = city(p(3.14, 101.69, "MY"), "kuala lumpur", "kul")
	bangkok     = city(p(13.76, 100.50, "TH"), "bangkok", "bkk")
	sydney      = city(p(-33.87, 151.21, "AU"), "sydney", "syd")
	melbourne   = city(p(-37.81, 144.96, "AU"), "melbourne", "mel")
	auckland    = city(p(-36.85, 174.76, "NZ"), "auckland", "akl")
	newyork     = city(p(40.71, -74.01, "US"), "new york", "newyork", "nyc", "jfk")
	newark      = city(p(40.74, -74.17, "US"), "newark", "new jersey", "ewr")
	ashburn     = city(p(39.04, -77.49, "US"), "ashburn", "virginia", "iad")
	washington  = city(p(38.90, -77.04, "US"), "washington")
	chicago     = city(p(41.88, -87.63, "US"), "chicago", "ord")
	dallas      = city(p(32.78, -96.80, "US"), "dallas", "dfw")
	atlanta     = city(p(33.75, -84.39, "US"), "atlanta", "atl")
	miami       = city(p(25.76, -80.19, "US"), "miami", "mia")
	seattle     = city(p(47.61, -122.33, "US"), "seattle", "sea")
	hillsboro   = city(p(45.52, -122.99, "US"), "hillsboro", "oregon", "hil")
	sanjose     = city(p(37.34, -121.89, "US"), "san jose", "silicon valley", "sjc")
	sanfran     = city(p(37.77, -122.42, "US"), "san francisco", "sfo")
	fremont     = city(p(37.55, -121.99, "US"), "fremont")
	losangeles  = city(p(34.05, -118.24, "US"), "los angeles", "lax")
	honolulu    = city(p(21.31, -157.86, "US"), "honolulu", "hnl")
	toronto     = city(p(43.65, -79.38, "CA"), "toronto", "yto", "yyz", "tor")
	montreal    = city(p(45.50, -73.57, "CA"), "montreal", "yul")
	beauharnois = city(p(45.31, -73.87, "CA"), "beauharnois", "bhs")
	vancouver   = city(p(49.28, -123.12, "CA"), "vancouver", "yvr")
	mexicocity  = city(p(19.43, -99.13, "MX"), "mexico city", "mex")
	saopaulo    = city(p(-23.55, -46.63, "BR"), "sao paulo", "são paulo", "sao", "gru")
	santiago    = city(p(-33.45, -70.67, "CL"), "santiago", "scl")
	johannesbrg = city(p(-26.20, 28.05, "ZA"), "johannesburg", "jnb")
)

// countries maps a country to its main datacenter city: the last resort for a
// region known only by its country.
var countries = map[string]Point{
	"DE": frankfurt, "NL": amsterdam, "GB": london, "UK": london, "FR": paris,
	"FI": helsinki, "SE": stockholm, "NO": oslo, "DK": copenhagen, "PL": warsaw,
	"ES": madrid, "IT": milan, "AT": vienna, "CH": zurich, "BE": brussels,
	"IE": dublin, "PT": lisbon, "CZ": prague, "RO": bucharest, "BG": sofia,
	"HU": budapest, "GR": athens, "TR": istanbul, "RU": moscow, "UA": kyiv,
	"LT": vilnius, "LV": riga, "EE": tallinn, "RS": belgrade, "LU": luxembourg,
	"MD": p(47.01, 28.86, "MD"), "SK": p(48.15, 17.11, "SK"), "SI": p(46.06, 14.51, "SI"),
	"HR": p(45.81, 15.98, "HR"), "IS": p(64.15, -21.94, "IS"), "CY": p(35.19, 33.38, "CY"),
	"IL": telaviv, "AE": dubai, "SA": riyadh, "QA": doha, "IR": tehran,
	"BH": p(26.23, 50.59, "BH"), "GE": tbilisi, "AM": yerevan, "AZ": baku, "KZ": almaty,
	"IN": mumbai, "SG": singapore, "JP": tokyo, "KR": seoul, "HK": hongkong,
	"TW": taipei, "ID": jakarta, "MY": kualalumpur, "TH": bangkok,
	"VN": p(10.82, 106.63, "VN"), "PH": p(14.60, 120.98, "PH"), "CN": p(31.23, 121.47, "CN"),
	"AU": sydney, "NZ": auckland, "US": ashburn, "CA": toronto, "MX": mexicocity,
	"BR": saopaulo, "CL": santiago, "AR": p(-34.60, -58.38, "AR"), "CO": p(4.71, -74.07, "CO"),
	"ZA": johannesbrg, "NG": p(6.52, 3.38, "NG"), "EG": p(30.04, 31.24, "EG"), "KE": p(-1.29, 36.82, "KE"),
}

// regions lists the region codes of each provider type. Codes not listed here
// are placed by city name, then by country.
var regions = map[string]map[string]Point{
	"hetzner": {
		"fsn1": falkenstein, "nbg1": nuremberg, "hel1": helsinki,
		"ash": ashburn, "hil": hillsboro, "sin": singapore,
	},
	"vultr": {
		"ewr": newark, "ord": chicago, "dfw": dallas, "sea": seattle, "lax": losangeles,
		"atl": atlanta, "ams": amsterdam, "lhr": london, "fra": frankfurt, "sjc": sanjose,
		"syd": sydney, "yto": toronto, "cdg": paris, "nrt": tokyo, "icn": seoul,
		"mia": miami, "sgp": singapore, "sto": stockholm, "mex": mexicocity, "mad": madrid,
		"sao": saopaulo, "del": delhi, "hnl": honolulu, "bom": mumbai, "blr": bangalore,
		"waw": warsaw, "jnb": johannesbrg, "itm": osaka, "mel": melbourne, "tlv": telaviv,
		"scl": santiago, "man": manchester,
	},
	"digitalocean": {
		"nyc1": newyork, "nyc2": newyork, "nyc3": newyork, "sfo1": sanfran, "sfo2": sanfran,
		"sfo3": sanfran, "ams2": amsterdam, "ams3": amsterdam, "sgp1": singapore,
		"lon1": london, "fra1": frankfurt, "tor1": toronto, "blr1": bangalore,
		"syd1": sydney, "atl1": atlanta,
	},
	"linode": {
		"us-east": newark, "us-central": dallas, "us-west": fremont, "us-southeast": atlanta,
		"ca-central": toronto, "eu-west": london, "eu-central": frankfurt,
		"ap-south": singapore, "ap-northeast": tokyo, "ap-west": mumbai,
		"ap-southeast": sydney, "us-iad": washington, "us-ord": chicago, "fr-par": paris,
		"us-sea": seattle, "br-gru": saopaulo, "nl-ams": amsterdam, "se-sto": stockholm,
		"es-mad": madrid, "in-maa": chennai, "jp-osa": osaka, "it-mil": milan,
		"us-mia": miami, "id-cgk": jakarta, "us-lax": losangeles, "gb-lon": london,
		"au-mel": melbourne, "in-bom-2": mumbai, "de-fra-2": frankfurt,
		"sg-sin-2": singapore, "jp-tyo-3": tokyo,
	},
	"ovh": {
		"de": frankfurt, "uk": london, "us-east-va": ashburn, "us-west-or": hillsboro,
		"eu-west-par": paris, "eu-south-mil": milan,
	},
}

// Locate places a provider region. hint is any text that may name its city
// (the region's display name); country is its ISO code when known. The second
// result says how exact the point is ("" when it could not be placed).
func Locate(provType, region, hint, country string) (Point, string) {
	r := strings.ToLower(strings.TrimSpace(region))
	tbl := regions[strings.ToLower(provType)]
	if pt, ok := tbl[r]; ok {
		return pt, ByRegion
	}
	// OVH names its regions "GRA11", "SBG5", "DE1": drop the trailing digits.
	if pt, ok := tbl[strings.TrimRight(r, "0123456789")]; ok {
		return pt, ByRegion
	}
	cc := strings.ToUpper(strings.TrimSpace(country))
	if pt, ok := byCity(r+" "+hint, cc); ok {
		return pt, ByCity
	}
	if pt, ok := countries[cc]; ok {
		return pt, ByCountry
	}
	return Point{}, ""
}

// byCity looks for a known city name or airport code in s. A match in another
// country than the one given is ignored ("par" in a German region's name).
func byCity(s, cc string) (Point, bool) {
	s = strings.ToLower(s)
	for n, pt := range cities {
		if strings.Contains(n, " ") && strings.Contains(s, n) && (cc == "" || pt.Country == cc) {
			return pt, true
		}
	}
	for _, tok := range strings.FieldsFunc(s, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c > 127)
	}) {
		if pt, ok := cities[tok]; ok && (cc == "" || pt.Country == cc) {
			return pt, true
		}
	}
	return Point{}, false
}

// Distance is the great-circle distance between two points, in km.
func Distance(a, b Point) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLon := (b.Lon - a.Lon) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}

// Ranked is a region of the target provider with its distance from the source.
type Ranked struct {
	providers.Option
	Km      float64 `json:"km"`
	Country string  `json:"country"`
	// Match says how the region was placed; "" when it could not be, and it
	// then ranks last with no distance.
	Match string `json:"match"`
}

// Nearest ranks the regions of a provider of type provType by their distance
// from src. Regions in src's country win ties; unplaced regions come last.
func Nearest(src Point, provType string, opts []providers.Option) []Ranked {
	out := make([]Ranked, 0, len(opts))
	for _, o := range opts {
		pt, how := Locate(provType, o.ID, o.Name+" "+o.Description, o.Country)
		cc := firstNonEmpty(strings.ToUpper(o.Country), pt.Country)
		rk := Ranked{Option: o, Country: cc, Match: how}
		if how != "" {
			rk.Km = math.Round(Distance(src, pt))
		}
		out = append(out, rk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Match == "") != (b.Match == "") {
			return b.Match == ""
		}
		if a.Km != b.Km {
			return a.Km < b.Km
		}
		return a.Country == src.Country && b.Country != src.Country
	})
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
