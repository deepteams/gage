---
name: go-table-tests
description: House style for writing Go table-driven tests. Load before writing or refactoring Go tests.
---

# Go table-driven tests

When writing or extending Go tests in this workspace:

- Prefer a table of cases over repeated test functions:

```go
func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Value
		wantErr bool
	}{
		{name: "empty input", in: "", wantErr: true},
		{name: "single field", in: "a=1", want: Value{A: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Parse(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
```

- Name cases in plain words describing the behavior, not `case1`.
- Use `t.Fatalf` for setup/short-circuit failures, `t.Errorf` for assertions
  that can accumulate.
- Use `t.TempDir()` for filesystem fixtures; never write outside it.
- Keep one behavior per test function; a growing `if tt.special` ladder means
  the table should be split.
- Finish by running the package's tests with `-race` and reporting the output.
