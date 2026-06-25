package main

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input     string
		wantMajor int
		wantMinor int
		wantOK    bool
	}{
		{"8.4", 8, 4, true},
		{"8.10", 8, 10, true},
		{"7.4", 7, 4, true},
		{"8.4.0", 8, 4, true},
		{"8.4.*", 8, 4, true}, // wildcard in patch is ignored, major.minor still valid
		{"  8.4  ", 8, 4, true},
		{"8", 0, 0, false},
		{"", 0, 0, false},
		{"abc", 0, 0, false},
		{"8.x", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			major, minor, ok := parseVersion(tt.input)
			if ok != tt.wantOK {
				t.Errorf("parseVersion(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
				return
			}
			if ok && (major != tt.wantMajor || minor != tt.wantMinor) {
				t.Errorf("parseVersion(%q) = (%d, %d), want (%d, %d)", tt.input, major, minor, tt.wantMajor, tt.wantMinor)
			}
		})
	}
}

func TestPhpVersionSatisfies(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		constraint string
		want       bool
	}{
		// Caret (^)
		{"caret match exact", "8.4", "^8.4", true},
		{"caret match higher minor", "8.5", "^8.4", true},
		{"caret reject lower minor", "8.3", "^8.4", false},
		{"caret reject different major", "9.0", "^8.4", false},
		{"caret with patch", "8.4", "^8.4.0", true},
		{"caret higher minor with patch", "8.10", "^8.4.0", true},

		// Tilde (~)
		{"tilde match exact", "8.4", "~8.4", true},
		{"tilde match higher minor", "8.5", "~8.4", true},
		{"tilde reject lower minor", "8.3", "~8.4", false},
		{"tilde reject different major", "9.0", "~8.4", false},

		// Greater or equal (>=)
		{"gte exact match", "8.4", ">=8.4", true},
		{"gte higher minor", "8.5", ">=8.4", true},
		{"gte higher major", "9.0", ">=8.4", true},
		{"gte reject lower", "8.3", ">=8.4", false},

		// Greater than (>)
		{"gt higher minor", "8.5", ">8.4", true},
		{"gt reject exact", "8.4", ">8.4", false},
		{"gt reject lower", "8.3", ">8.4", false},

		// Less than (<)
		{"lt lower minor", "8.3", "<8.4", true},
		{"lt reject exact", "8.4", "<8.4", false},
		{"lt reject higher", "8.5", "<8.4", false},

		// Less or equal (<=)
		{"lte exact", "8.4", "<=8.4", true},
		{"lte lower", "8.3", "<=8.4", true},
		{"lte reject higher", "8.5", "<=8.4", false},

		// Not equal (!=)
		{"neq different", "8.3", "!=8.4", true},
		{"neq same", "8.4", "!=8.4", false},

		// Exact
		{"exact match", "8.4", "8.4", true},
		{"exact mismatch", "8.3", "8.4", false},

		// OR (||)
		{"or first matches", "8.2", "^8.2 || ^8.3", true},
		{"or second matches", "8.3", "^8.2 || ^8.3", true},
		{"or neither matches", "7.4", "^8.2 || ^8.3", false},

		// AND (space-separated)
		{"and both match", "8.4", ">=8.2 <9.0", true},
		{"and first fails", "8.1", ">=8.2 <9.0", false},
		{"and second fails", "9.0", ">=8.2 <9.0", false},

		// Unparseable — should not produce false positives
		{"unparseable constraint", "8.4", ">=8.x", true},
		{"unparseable version", "abc", "^8.4", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := phpVersionSatisfies(tt.version, tt.constraint)
			if got != tt.want {
				t.Errorf("phpVersionSatisfies(%q, %q) = %v, want %v", tt.version, tt.constraint, got, tt.want)
			}
		})
	}
}
