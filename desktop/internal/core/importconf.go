package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// countryTZ gives a sensible browser timezone for common VPN exit countries.
var countryTZ = map[string]string{
	"US": "America/New_York", "CA": "America/Toronto", "MX": "America/Mexico_City", "BR": "America/Sao_Paulo",
	"AR": "America/Argentina/Buenos_Aires", "GB": "Europe/London", "UK": "Europe/London", "IE": "Europe/Dublin",
	"FR": "Europe/Paris", "DE": "Europe/Berlin", "NL": "Europe/Amsterdam", "BE": "Europe/Brussels",
	"ES": "Europe/Madrid", "IT": "Europe/Rome", "CH": "Europe/Zurich", "AT": "Europe/Vienna", "SE": "Europe/Stockholm",
	"NO": "Europe/Oslo", "DK": "Europe/Copenhagen", "FI": "Europe/Helsinki", "PL": "Europe/Warsaw",
	"PT": "Europe/Lisbon", "TR": "Europe/Istanbul", "AE": "Asia/Dubai", "IL": "Asia/Jerusalem", "IN": "Asia/Kolkata",
	"SG": "Asia/Singapore", "MY": "Asia/Kuala_Lumpur", "TH": "Asia/Bangkok", "VN": "Asia/Ho_Chi_Minh",
	"ID": "Asia/Jakarta", "PH": "Asia/Manila", "HK": "Asia/Hong_Kong", "TW": "Asia/Taipei", "JP": "Asia/Tokyo",
	"KR": "Asia/Seoul", "CN": "Asia/Shanghai", "AU": "Australia/Sydney", "NZ": "Pacific/Auckland",
	"ZA": "Africa/Johannesburg", "EG": "Africa/Cairo", "NG": "Africa/Lagos",
}

var nameRe = regexp.MustCompile(`[^a-z0-9-]+`)

// ImportConf copies a WireGuard .conf into the config's secrets folder and adds a profile for it.
// Country-city names (uk-lon.conf, us-nyc.conf, as many providers use) give the country and timezone; both stay editable.
func ImportConf(cfg *Config, src string) (*Profile, error) {
	if _, err := ParseWGConf(src); err != nil {
		return nil, err
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
	name := strings.Trim(nameRe.ReplaceAllString(base, "-"), "-")
	if name == "" {
		name = "wg"
	}
	for i := 2; cfg.Profile[name] != nil; i++ {
		name = fmt.Sprintf("%s-%d", strings.Trim(nameRe.ReplaceAllString(base, "-"), "-"), i)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	rel := filepath.Join("secrets", name+".conf")
	if err := os.MkdirAll(filepath.Join(cfg.Dir(), "secrets"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfg.Resolve(rel), data, 0o600); err != nil {
		return nil, err
	}
	p := &Profile{Name: name, Type: "gluetun", Provider: "custom", VPN: "wireguard", WGConf: rel, Port: cfg.FreePort(8890)}
	if cc := strings.ToUpper(strings.SplitN(name, "-", 2)[0]); len(cc) == 2 {
		if cc == "UK" {
			cc = "GB"
		}
		p.ExpectCountry = cc
		p.TZ = countryTZ[cc]
	}
	cfg.Profile[name] = p
	return p, cfg.Save()
}
