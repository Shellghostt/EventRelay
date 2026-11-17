package config

import "testing"

func TestDashboardOriginValidation(t *testing.T) {
	for _, row := range []struct{ raw, want string }{
		{"http://localhost:8080", "http://localhost:8080"}, {"https://Console.Example/", "https://console.example"},
		{"https://console.example:443/", "https://console.example"}, {"http://localhost:80", "http://localhost"},
		{"https://[::1]:443", "https://[::1]"},
	} {
		t.Run(row.raw, func(t *testing.T) {
			got, err := ValidateDashboardOrigin(row.raw)
			if err != nil || got != row.want {
				t.Fatalf("origin=%q error=%v want=%q", got, err, row.want)
			}
		})
	}
	for _, raw := range []string{"", "//localhost", "https://", "ftp://localhost", "https://user:secret@console.example", "https://console.example/path", "https://console.example?query=1", "https://console.example#fragment", "http://localhost:99999", "http://localhost:0", "http://localhost:"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ValidateDashboardOrigin(raw); err == nil {
				t.Fatal("invalid origin accepted")
			}
		})
	}
}
