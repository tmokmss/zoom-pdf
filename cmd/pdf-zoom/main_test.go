package main

import (
	"math"
	"testing"
)

func TestParseBbox(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    [4]float64
		wantErr bool
	}{
		{"full page", "0,0,1,1", [4]float64{0, 0, 1, 1}, false},
		{"fractional", "0.25,0.5,0.75,1", [4]float64{0.25, 0.5, 0.75, 1}, false},
		{"with spaces", " 0 , 0.1 , 0.2 , 0.3 ", [4]float64{0, 0.1, 0.2, 0.3}, false},
		{"too few", "1,2,3", [4]float64{}, true},
		{"too many", "0,0,1,1,2", [4]float64{}, true},
		{"non-numeric", "a,b,c,d", [4]float64{}, true},
		{"out of range high", "0,0,2,1", [4]float64{}, true},
		{"out of range low", "-0.1,0,1,1", [4]float64{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBbox(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseBbox(%q) err = %v; wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			for i := range got {
				if math.Abs(got[i]-tt.want[i]) > 1e-9 {
					t.Errorf("parseBbox(%q)[%d] = %v; want %v", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}
