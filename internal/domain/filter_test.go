package domain

import "testing"

func TestListParams_WithDefaults(t *testing.T) {
	tests := []struct {
		name       string
		input      ListParams
		wantLimit  int
		wantOffset int
	}{
		{
			name:       "zero value gets defaults",
			input:      ListParams{},
			wantLimit:  100,
			wantOffset: 0,
		},
		{
			name:       "large limit capped at 1000",
			input:      ListParams{Limit: 5000, Offset: 0},
			wantLimit:  1000,
			wantOffset: 0,
		},
		{
			name:       "negative limit set to 100",
			input:      ListParams{Limit: -1, Offset: 0},
			wantLimit:  100,
			wantOffset: 0,
		},
		{
			name:       "negative offset clamped to 0",
			input:      ListParams{Limit: 50, Offset: -1},
			wantLimit:  50,
			wantOffset: 0,
		},
		{
			name:       "valid values preserved",
			input:      ListParams{Limit: 50, Offset: 10},
			wantLimit:  50,
			wantOffset: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.WithDefaults()
			if got.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", got.Limit, tt.wantLimit)
			}
			if got.Offset != tt.wantOffset {
				t.Errorf("Offset = %d, want %d", got.Offset, tt.wantOffset)
			}
		})
	}
}
