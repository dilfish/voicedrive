package main

import "testing"

func TestControlEndpoint(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"http", "http://localhost:8080", "ws://localhost:8080/v1/device"},
		{"https", "https://example.test/api/", "wss://example.test/api/v1/device"},
		{"ws", "ws://localhost:8080/", "ws://localhost:8080/v1/device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := controlEndpoint(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("controlEndpoint(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestControlEndpointRejectsInvalidURL(t *testing.T) {
	for _, input := range []string{"", "localhost:8080", "ftp://localhost:8080"} {
		if _, err := controlEndpoint(input); err == nil {
			t.Errorf("controlEndpoint(%q) accepted invalid URL", input)
		}
	}
}
