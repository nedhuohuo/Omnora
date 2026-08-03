package storage

import "testing"

func TestCleanRelativePath(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"empty is root", "", ".", false},
		{"normalizes", "docs/./a.txt", "docs/a.txt", false},
		{"rejects absolute", "/data/a", "", true},
		{"rejects traversal", "../a", "", true},
		{"rejects reserved root", ".omnora", "", true},
		{"rejects reserved subtree", ".omnora/trash/a", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanRelativePath(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CleanRelativePath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("CleanRelativePath() = %q, want %q", got, tt.want)
			}
		})
	}
}
